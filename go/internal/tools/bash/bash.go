// Package bash ports src/tools/bash.ts: the cave__bash tool plus the shell
// helpers the CLI reuses (`cave-tools filter --json` uses FilterShellOutput,
// and the CLI bash passthrough uses CliShell and ShellExitCode).
package bash

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"strconv"
	"strings"
	"syscall"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// port of src/tools/bash.ts:DEFAULT_TIMEOUT
const defaultTimeout = 120000

// port of src/tools/bash.ts:MAX_TIMEOUT
const maxTimeout = 10 * 60 * 1000

// Tool is the cave__bash MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__bash",
	Description: "Shell with RTK rewriting (never prefix rtk yourself), secret redaction, output budget; archives what it trims. On Claude Code/opencode native shell output is already compressed (successful commands); this also compresses failures.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string","description":"5-10 word summary"},"workdir":{"type":"string","description":"Use instead of cd"},"timeout":{"type":"number","description":"ms; 0 disables, max 600000","default":120000},"allowFailure":{"type":"boolean","description":"Return non-zero exits as text, not errors","default":false},"redact_secrets":{"type":"boolean","default":true}},"required":["command","description"]}`),
	Handler:     handle,
}

// cliShellRE is the /\/(bash|zsh)$/ test in cliShell.
var cliShellRE = jsre.MustCompile(`\/(bash|zsh)$`, "")

// CliShell wraps an agent's native shell call, so run it in the same kind of
// shell the agent used (bash/zsh), not /bin/sh, which may be dash.
// port of src/tools/bash.ts:cliShell
func CliShell(ctx context.Context) string {
	shell := rt.GetEnv(ctx)["SHELL"]
	if !isWindows() && shell != "" && cliShellRE.MatchString(shell) {
		return shell
	}
	return rt.DefaultShell(ctx)
}

// ShellExitCode renders a shell-style exit status: real code, 124 on timeout,
// 128+n on signal.
// port of src/tools/bash.ts:shellExitCode
func ShellExitCode(result rt.RunCommandResult) int {
	if result.TimedOut {
		return 124
	}
	if result.Signal != nil {
		return 128 + signalNumber(*result.Signal)
	}
	if result.ExitCode != nil {
		return *result.ExitCode
	}
	return 1
}

// ShellOutput is the post-run pipeline input. ExitCode is accepted for
// callers' completeness; the pipeline does not branch on it yet.
// port of src/tools/bash.ts:ShellOutput
type ShellOutput struct {
	Command string
	Stdout  string
	Stderr  string
	// ExitCode is accepted for callers' completeness; unused by the pipeline.
	ExitCode *int
	// RewrittenCommand is the command actually run (after rtk rewrite); it
	// drives structured extraction. nil means Command.
	RewrittenCommand *string
	// Redact defaults to true when nil, like TS's `input.redact !== false`.
	Redact *bool
}

// FilteredShellOutput is filterShellOutput's result.
type FilteredShellOutput struct {
	Stdout string
	Stderr string
}

// FilterShellOutput is the post-run pipeline shared by cave__bash, its CLI mode
// and `cave-tools filter`: classify by command, redact, structured extraction,
// repetition collapse, budget, archive. stdout comes back compressed (plus an
// archive handle when trimmed); stderr only redacted and budgeted.
// port of src/tools/bash.ts:filterShellOutput
func FilterShellOutput(ctx context.Context, input ShellOutput) FilteredShellOutput {
	command := input.Command
	shouldRedact := input.Redact == nil || *input.Redact
	rewritten := command
	if input.RewrittenCommand != nil {
		rewritten = *input.RewrittenCommand
	}
	redact := func(text string) string {
		if shouldRedact {
			return compression.RedactSecrets(text)
		}
		return text
	}
	stderr := ""
	if input.Stderr != "" {
		stderr = compression.ApplyBudget(ctx, redact(input.Stderr), "bash")
	}
	policy := compression.ClassifyCommand(command)
	processed := redact(input.Stdout)

	if policy == compression.PolicyPassthrough {
		return FilteredShellOutput{Stdout: processed, Stderr: stderr}
	}

	// Full (already redacted) output, archived behind a handle if trimmed.
	full := processed

	if policy == compression.PolicyVerbatim {
		lines := strings.Split(processed, "\n")
		const verbatimMax = 500
		if len(lines) > verbatimMax {
			trimmed := make([]string, 0, 501)
			trimmed = append(trimmed, lines[:250]...)
			trimmed = append(trimmed, "\n... ("+strconv.Itoa(len(lines)-500)+" lines truncated) ...\n")
			trimmed = append(trimmed, lines[len(lines)-250:]...)
			processed = strings.Join(trimmed, "\n")
		}
		return FilteredShellOutput{
			Stdout: processed + compression.ArchiveHandle(full, processed, command),
			Stderr: stderr,
		}
	}

	// Try semantic structured compression first; fall back to the
	// whitespace-only extractor when input is non-structured or the
	// semantic pass returned null (compression didn't help / toggle off).
	if semantic, ok := compression.CompressStructuredSemantic(ctx, processed, rewritten); ok {
		processed = semantic
	} else {
		processed = compression.ExtractStructuredData(processed, rewritten)
	}

	// Collapse runs of identical lines before the line budget, so the
	// budget sees compacted content. Exact-match only (digit-mask opt-in
	// lives on collapseRepetition; off here to avoid eating request IDs).
	processed = compression.CollapseRepetition(processed)

	// Apply budget compression
	processed = compression.ApplyBudget(ctx, processed, "bash")

	return FilteredShellOutput{
		Stdout: processed + compression.ArchiveHandle(full, processed, command),
		Stderr: stderr,
	}
}

// handle is the cave__bash handler.
// port of src/tools/bash.ts:bashTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	command := jsArgString(args, "command")
	var workdir string
	if raw, ok := args["workdir"]; ok && jsTruthy(raw) {
		workdir = jsString(raw)
	}
	allowFailure := isTrue(args["allowFailure"])
	shouldRedact := !isFalse(args["redact_secrets"])
	// Set by the CLI only: stdout alone is compressed into the text, stderr and
	// the exit code travel in _meta.caveCli so the CLI can mirror the child.
	cliMode := isTrue(args["__cli"])

	// Validate timeout: reject negative/NaN, fall back to default when
	// missing. 0 disables the timeout entirely (matches opencode shell).
	rawTimeout, hasTimeout := args["timeout"]
	timeout := float64(defaultTimeout)
	if hasTimeout {
		timeout = jsNumber(rawTimeout)
	}
	if math.IsNaN(timeout) || math.IsInf(timeout, 0) || timeout < 0 {
		return errorResult("Invalid timeout value: " + jsString(rawTimeout) +
			". Timeout must be a non-negative number."), nil
	}
	if timeout > maxTimeout {
		return errorResult("Invalid timeout value: " + jsString(rawTimeout) +
			". Timeout must be <= " + strconv.Itoa(maxTimeout) + "ms."), nil
	}

	rewrittenCommand := command

	cwd, err := rt.ResolveExistingDirectory(ctx, workdir)
	if err != nil {
		return launchFailure(ctx, command, rewrittenCommand, pathErrorMessage(err), shouldRedact, allowFailure), nil
	}
	peeledCommand, stripped := compression.StripLeadingRtk(command)
	rewrittenCommand = compression.RewriteCommandWithRtk(ctx, peeledCommand, stripped)
	shell := rt.DefaultShell(ctx)
	if cliMode {
		shell = CliShell(ctx)
	}
	// Node clamps a timer below 1ms to 1ms and truncates the rest
	// (setTimeout(1.5) fires after 1); a plain int64 cast would turn 0.5 into
	// "no timeout at all".
	timeoutMillis := int64(math.Floor(timeout))
	if timeout > 0 && timeoutMillis < 1 {
		timeoutMillis = 1
	}
	result, err := rt.RunCommand(ctx, rewrittenCommand, rt.RunCommandOptions{
		Timeout:  timeoutMillis,
		Cwd:      cwd,
		Shell:    shell,
		MaxBytes: 100 * 1024, // generous; cave's budget table tightens further downstream
		MaxLines: 2000,
	})
	if err != nil {
		return launchFailure(ctx, command, rewrittenCommand, err.Error(), shouldRedact, allowFailure), nil
	}

	// Exit code as data: non-zero (and timeout/signal) are reported inline
	// rather than thrown, so partial output is never lost.
	exitCode := ShellExitCode(result)
	exitFailed := result.TimedOut ||
		(result.ExitCode != nil && *result.ExitCode != 0) ||
		result.Signal != nil
	captureNote := ""
	if capture := rt.CaptureNotice(result.StdoutTruncated, result.StderrTruncated); capture != nil {
		captureNote = "\n\n" + *capture
	}
	exitNote := ""
	switch {
	case result.TimedOut:
		exitNote = "\n\n[timed out after " + jsNumberString(timeout) + "ms - process group killed]"
	case result.Signal != nil:
		exitNote = "\n\n[killed by signal " + *result.Signal + "]"
	case result.ExitCode != nil && *result.ExitCode != 0:
		exitNote = "\n\n[exit: " + strconv.Itoa(*result.ExitCode) + "]"
	}

	stdout := result.Output
	stderr := ""
	if cliMode {
		stdout = result.Stdout
		stderr = result.Stderr
	}
	redact := shouldRedact
	filtered := FilterShellOutput(ctx, ShellOutput{
		Command:          command,
		RewrittenCommand: &rewrittenCommand,
		Stdout:           stdout,
		Stderr:           stderr,
		ExitCode:         &exitCode,
		Redact:           &redact,
	})
	markError := exitFailed && !allowFailure
	if !cliMode {
		// MCP folds stderr into the text; CLI keeps it apart for fd 2.
		return types.ToolResult{
			Content: []types.Content{types.Text(filtered.Stdout + captureNote + exitNote)},
			IsError: markError,
		}, nil
	}
	// CLI: notes go to stderr, never into stdout.
	notes := captureNote
	if result.TimedOut || result.Signal != nil {
		notes += exitNote
	}
	notes = jsre.Trim(notes)
	parts := make([]string, 0, 2)
	if filtered.Stderr != "" {
		parts = append(parts, filtered.Stderr)
	}
	if notes != "" {
		parts = append(parts, notes)
	}
	separator := "\n"
	if strings.HasSuffix(filtered.Stderr, "\n") {
		separator = ""
	}
	return types.ToolResult{
		Content: []types.Content{types.Text(filtered.Stdout)},
		IsError: markError,
		Meta: map[string]any{
			"caveCli": map[string]any{
				"exitCode": exitCode,
				"stderr":   strings.Join(parts, separator),
			},
		},
	}, nil
}

// launchFailure builds the catch-block result.
// port of src/tools/bash.ts:bashTool.handler (catch block)
func launchFailure(ctx context.Context, command, rewrittenCommand, message string, shouldRedact, allowFailure bool) types.ToolResult {
	errorMessage := strings.Join([]string{
		"Command failed to launch",
		"Original command: " + command,
		"Executed command: " + rewrittenCommand,
		"Message: " + message,
	}, "\n")
	if shouldRedact {
		errorMessage = compression.RedactSecrets(errorMessage)
	}
	errorMessage = compression.ApplyBudget(ctx, errorMessage, "bash")
	return types.ToolResult{
		Content: []types.Content{types.Text(errorMessage)},
		IsError: !allowFailure,
	}
}

// pathErrorMessage renders fs.stat/fs.realpath failures the way Node does
// (`ENOENT: no such file or directory, stat '<path>'`), so the catch-block
// text matches the TS reference.
func pathErrorMessage(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		code := ""
		description := ""
		switch {
		case errors.Is(err, fs.ErrNotExist):
			code, description = "ENOENT", "no such file or directory"
		case errors.Is(err, fs.ErrPermission):
			code, description = "EACCES", "permission denied"
		case errors.Is(err, syscall.ENOTDIR):
			code, description = "ENOTDIR", "not a directory"
		case errors.Is(err, syscall.ELOOP):
			code, description = "ELOOP", "too many symbolic links encountered"
		}
		if code != "" {
			return code + ": " + description + ", " + pathErr.Op + " '" + pathErr.Path + "'"
		}
	}
	return err.Error()
}

// errorResult builds a text-only error result.
func errorResult(text string) types.ToolResult {
	return types.ToolResult{
		Content: []types.Content{types.Text(text)},
		IsError: true,
	}
}

// isTrue reports whether value is the JSON boolean true (TS `=== true`).
func isTrue(value any) bool {
	boolean, ok := value.(bool)
	return ok && boolean
}

// isFalse reports whether value is the JSON boolean false (TS `=== false`).
func isFalse(value any) bool {
	boolean, ok := value.(bool)
	return ok && !boolean
}

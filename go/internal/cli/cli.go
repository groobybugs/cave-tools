// Package cli is the command-line entry layer: argument dispatch, CLI-only
// tools and the status renderer.
package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/daemon"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// usageLineCLI is the top-level usage error for unknown commands.
// port of src/cli.ts:else usage branch
const usageLineCLI = "Usage: cave-tools [mcp|bench|install-agent|init|status [--emit-statusline|--verbose]|errors [--limit N]|jobs [kill <id> [signal]|log <id> [lines]]|bins|daemon [start|stop|status]|raw <command...>|filter --json [--session <id>]|help [tool]|<tool> [args...]|tool <tool> [args...]]"

// caveInstructions is the CLAUDE.md/AGENTS.md text `init` writes.
// port of src/cli.ts:CAVE_INSTRUCTIONS
const caveInstructions = "# Cave Tools — token-optimized MCP\n" +
	"\n" +
	"Cave Tools provides optimized drop-in replacements for shell + file ops (RTK\n" +
	"rewriting, structured JSON/XML extraction, per-tool line budgets). Same results,\n" +
	"fewer tokens. Prefer file/web/edit replacements over built-ins; use shell routing below.\n" +
	"\n" +
	"## Tools\n" +
	"- `cave__read` instead of Read — optimized drop-in replacement (dedup cache + line budgets).\n" +
	"- `cave__grep`, `cave__find`, `cave__ls` instead of shell/Glob/Grep.\n" +
	"- Shell: native shell is the default on Claude Code / opencode, with output auto-compressed through the CLI filter (Claude: successful commands only). Use `cave__bash` for RTK pre-rewrite or guaranteed failure compression — never prefix `cave-tools` or `rtk` yourself. Other hosts: use `cave__bash` (max 10min), or `cave-tools bash -- '<cmd>'` from the shell if MCP is down.\n" +
	"- Long jobs: native background for servers/watchers (uncompressed); `cave__bash_start` when a long job's final output should be compressed — poll `cave__bash_status` (`wait` up to 60s), stop `cave__bash_stop`. `cave-tools jobs` lists/kills jobs from a terminal.\n" +
	"- `[cave: full output … at <path>]`: `cave__read` offset/limit or `cave__grep` that path instead of re-running. Unfiltered: `cave-tools raw <cmd>` / `CAVE_RAW=1`. `CAVE_TOOLS_FILTER_BASH=0` disables the native filter; `CAVE_TOOLS_WRAP_BASH=1` wraps commands before they run (changes which permission/deny rules match).\n" +
	"- `cave__compress` to optimize large pasted or tool-produced text down to fewer tokens.\n" +
	"- `cave__status` to inspect savings, cache hit rate, reduction % + budgets.\n" +
	"- `cave__write` to create/overwrite a single file; `cave__edit` for fuzzy string replacement.\n" +
	"- After editing a file outside Cave Tools, call `cave__invalidate` to refresh the read dedup cache.\n" +
	"\n" +
	"## Rules\n" +
	"- Do not double-wrap: never run `rtk <cmd>` inside `cave__bash` (it already prepends rtk).\n" +
	"- Pass raw file paths / patterns / commands — do not wrap tools in extra scripts.\n" +
	"- Never `sleep`-poll to wait for long tasks.\n" +
	"- Interactive commands (vim, less, ssh) and hook-sensitive stdin: native shell.\n" +
	"\n" +
	"## Savings\n" +
	"Run `cave-tools status` (CLI) or `cave__status` (MCP) for reduction % and an\n" +
	"rtk-gain-style efficiency meter aggregated across live sessions.\n"

// Run executes one CLI invocation and returns its process exit code.
// port of src/cli.ts:top-level dispatch
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, version string) int {
	// The Go binary keeps the --version probe used by the parity harness; TS
	// has no such branch, so this is an intentional compatibility extension.
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "cave-tools-go %s\n", version)
		return 0
	}
	if len(args) == 0 || args[0] == "mcp" {
		return runMCPCommand(ctx, version, stdin, stdout, stderr)
	}

	switch args[0] {
	case "status":
		return runStatusCommand(args[1:], stdout)
	case "bench":
		fmt.Fprintln(stdout, "Benchmark mode - not yet implemented")
		return 0
	case "install-agent", "init":
		return runInitCommand(stdout, stderr)
	case "jobs":
		return runJobsCommand(ctx, args[1:], stdout, stderr)
	case "bins":
		return runBinsCommand(ctx, stdout)
	case "daemon":
		return runDaemonCommand(ctx, args[1:], stdout, stderr)
	case "errors":
		return rt.RunErrorsCommand(args[1:])
	case "help", "--help", "-h":
		return runHelpCommand(optionalArg(args, 1), stdout, stderr)
	case "raw":
		return runRawCommand(ctx, args[1:], stdin, stdout, stderr)
	case "filter":
		return runFilterCommand(ctx, args[1:], stdin, stdout, stderr)
	case "tool":
		var short *string
		if len(args) > 1 {
			value := args[1]
			short = &value
		}
		return runToolCommand(ctx, short, argsFrom(args, 2), stdin, stdout, stderr)
	default:
		if isToolNameRuntime("cave__" + args[0]) {
			value := args[0]
			return runToolCommand(ctx, &value, args[1:], stdin, stdout, stderr)
		}
		fmt.Fprintln(stderr, usageLineCLI)
		return 2
	}
}

// optionalArg returns args[index] when present.
func optionalArg(args []string, index int) *string {
	if index >= len(args) {
		return nil
	}
	value := args[index]
	return &value
}

// argsFrom returns args[index:] or nil when the index is past the end.
func argsFrom(args []string, index int) []string {
	if index >= len(args) {
		return nil
	}
	return args[index:]
}

// containsArg reports whether argv contains token.
func containsArg(argv []string, token string) bool {
	return containsString(argv, token)
}

// runMCPCommand starts the stdio MCP server.
// port of src/cli.ts:mcp branch
func runMCPCommand(ctx context.Context, version string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := startServerRuntime(ctx, version, stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "Fatal error: %v\n", err)
		return 1
	}
	return 0
}

// runStatusCommand implements `status [--emit-statusline|--verbose|-v]`.
// port of src/cli.ts:status branch
func runStatusCommand(args []string, stdout io.Writer) int {
	if containsArg(args, "--emit-statusline") {
		// Only the aggregates the badge needs: no rtk probe, no by-tool table.
		writeStatuslineSuffix(buildStatuslineSuffix())
		return 0
	}
	summary := buildStatusSummary()
	rtkSkipped := os.Getenv("CAVE_TOOLS_STATUS_RTK") == "0"
	var rtkGain *RtkGainSummary
	if !rtkSkipped {
		rtkGain = FetchRtkGainSummary()
	}
	verbose := containsArg(args, "--verbose") || containsArg(args, "-v")
	fmt.Fprintln(stdout, RenderStatusCLI(StatusRenderInput{
		RtkAvailable:          summary.RtkAvailable,
		ReductionPct:          summary.ReductionPct,
		HitRatePct:            summary.HitRatePct,
		CacheHits:             summary.CacheHits,
		CacheMisses:           summary.CacheMisses,
		FilesTracked:          summary.FilesTracked,
		RtkRewrites:           summary.RtkRewrites,
		RtkAlreadyWrapped:     summary.RtkAlreadyWrapped,
		RtkPassthrough:        summary.RtkPassthrough,
		TotalCalls:            summary.TotalCalls,
		RawChars:              summary.RawChars,
		CompressedChars:       summary.CompressedChars,
		CompressionSavedChars: summary.CompressionSavedChars,
		DedupSavedChars:       summary.DedupSavedChars,
		SavedChars:            summary.SavedChars,
		TokensSaved:           summary.TokensSaved,
		LiveSessions:          summary.LiveSessions,
		EndedSessions:         summary.EndedSessions,
		LastUpdate:            summary.LastUpdate,
		HasData:               summary.HasData,
		Budgets:               summary.Budgets,
		ByTool:                summary.ByTool,
		RtkGain:               rtkGain,
		RtkGainSkipped:        rtkSkipped,
		Verbose:               verbose,
	}))
	return 0
}

// runInitCommand writes CLAUDE.md/AGENTS.md only when missing.
// port of src/cli.ts:install-agent/init branch
func runInitCommand(stdout, stderr io.Writer) int {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "cave-tools init: %v\n", err)
		return 1
	}
	claude, err := writeIfMissing(filepath.Join(dir, "CLAUDE.md"), caveInstructions)
	if err != nil {
		fmt.Fprintf(stderr, "cave-tools init: %v\n", err)
		return 1
	}
	agents, err := writeIfMissing(filepath.Join(dir, "AGENTS.md"), caveInstructions)
	if err != nil {
		fmt.Fprintf(stderr, "cave-tools init: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Cave Tools instructions:")
	if claude == "created" {
		fmt.Fprintln(stdout, "  CLAUDE.md  created")
	} else {
		fmt.Fprintln(stdout, "  CLAUDE.md  already exists (left untouched)")
	}
	if agents == "created" {
		fmt.Fprintln(stdout, "  AGENTS.md  created")
	} else {
		fmt.Fprintln(stdout, "  AGENTS.md  already exists (left untouched)")
	}
	return 0
}

// writeIfMissing ports writeIfMissing's exists/created result.
// port of src/cli.ts:writeIfMissing
func writeIfMissing(file, content string) (string, error) {
	if _, err := os.Stat(file); err == nil {
		return "exists", nil
	}
	if err := os.WriteFile(file, []byte(content), 0o666); err != nil {
		return "", err
	}
	return "created", nil
}

// runJobsCommand implements `jobs [kill <id> [signal]|log <id> [lines]]`.
// port of src/cli.ts:jobs branch
func runJobsCommand(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	sub := ""
	if len(argv) > 0 {
		sub = argv[0]
	}
	if sub == "kill" {
		if len(argv) < 2 || argv[1] == "" {
			fmt.Fprintln(stderr, "Usage: cave-tools jobs kill <jobId> [signal]")
			return 1
		}
		jobID := argv[1]
		signal := "SIGTERM"
		if len(argv) > 2 {
			signal = argv[2]
		}
		job, err := rt.StopJob(jobID, signal)
		if err != nil {
			fmt.Fprintf(stderr, "cave-tools jobs: %v\n", err)
			return 1
		}
		if job == nil {
			fmt.Fprintf(stderr, "Unknown job: %s\n", jobID)
			return 1
		}
		if job.State == storage.JobKilled {
			fmt.Fprintf(stdout, "%s killed (sent %s to process group %d).\n", job.JobID, signal, job.Pid)
		} else {
			fmt.Fprintf(stdout, "%s already finished (state: %s).\n", job.JobID, job.State)
		}
		return 0
	}

	if sub == "log" {
		if len(argv) < 2 || argv[1] == "" {
			fmt.Fprintln(stderr, "Usage: cave-tools jobs log <jobId> [lines]")
			return 1
		}
		jobID := argv[1]
		job, err := rt.GetJobInfo(jobID)
		if err != nil {
			fmt.Fprintf(stderr, "cave-tools jobs: %v\n", err)
			return 1
		}
		if job == nil {
			fmt.Fprintf(stderr, "Unknown job: %s\n", jobID)
			return 1
		}
		wanted := 100.0
		if len(argv) > 2 {
			number := jsToNumber(argv[2])
			if number != 0 && !math.IsNaN(number) {
				wanted = math.Max(1, number)
			}
		}
		tail := rt.TailFileBytes(job.LogPath, 256*1024)
		lines := strings.Split(tail, "\n")
		text := tail
		if float64(len(lines)) > wanted {
			count := int(wanted)
			if count < 1 {
				count = 1
			}
			if count > len(lines) {
				count = len(lines)
			}
			text = strings.Join(lines[len(lines)-count:], "\n")
		}
		_, _ = io.WriteString(stdout, text)
		return 0
	}

	jobs, err := storage.ListJobs(nil)
	if err != nil {
		fmt.Fprintf(stderr, "cave-tools jobs: %v\n", err)
		return 1
	}
	if len(jobs) == 0 {
		fmt.Fprintln(stdout, "No background jobs recorded.")
		return 0
	}
	for _, listed := range jobs {
		job := listed
		if info, err := rt.GetJobInfo(listed.JobID); err == nil && info != nil {
			job = *info
		}
		end := time.Now().UnixMilli()
		if job.EndedAt != nil {
			end = *job.EndedAt
		}
		secs := jsMathRound(float64(end-job.StartedAt) / 1000)
		command := compression.RedactSecrets(job.Command)
		command = jsre.MustCompile(`\s+`, "g").ReplaceAllString(command, " ")
		command = jsre.Trim(command)
		short := command
		if jsUTF16Length(command) > 80 {
			short = jsSliceUTF16(command, 0, 77) + "..."
		}
		state := string(job.State)
		if job.State == storage.JobExited {
			exit := "?"
			if job.ExitCode != nil {
				exit = fmt.Sprintf("%d", *job.ExitCode)
			}
			state = "exited(" + exit + ")"
		}
		fmt.Fprintf(stdout, "%s  %s %s pid=%d  %s\n", job.JobID, padEndJS(state, 10), padEndJS(fmt.Sprintf("%ds", secs), 7), job.Pid, short)
		fmt.Fprintf(stdout, "  log: %s\n", job.LogPath)
	}
	return 0
}

// jsUTF16Length counts JavaScript string length units.
func jsUTF16Length(value string) int {
	length := 0
	for _, r := range value {
		length += utf16RuneLength(r)
	}
	return length
}

// jsSliceUTF16 mirrors String.prototype.slice(0, end) code-unit slicing.
func jsSliceUTF16(value string, start, end int) string {
	units := utf16Encode(value)
	if start > len(units) {
		start = len(units)
	}
	if end > len(units) {
		end = len(units)
	}
	if start >= end {
		return ""
	}
	return utf16Decode(units[start:end])
}

// runBinsCommand implements `bins`.
// port of src/cli.ts:bins branch
func runBinsCommand(ctx context.Context, stdout io.Writer) int {
	resolution := rt.EnsureBins(ctx, rt.EnsureBinsOptions{Log: func(message string) {
		fmt.Fprintln(stdout, message)
	}})
	fmt.Fprintf(stdout, "binDir: %s\n", rt.BinDir(ctx))
	fmt.Fprintf(stdout, "rg: %s\n", binsDisplay(resolution.Rg))
	fmt.Fprintf(stdout, "fd: %s\n", binsDisplay(resolution.Fd))
	if resolution.Rg != "" {
		return 0
	}
	return 1
}

// binsDisplay renders the empty-resolution sentinel.
// port of src/cli.ts:bins branch (`?? "(missing)"`)
func binsDisplay(path string) string {
	if path == "" {
		return "(missing)"
	}
	return path
}

// runDaemonCommand implements `daemon [start|stop|status]`.
// port of src/cli.ts:daemon branch
func runDaemonCommand(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	sub := "status"
	if len(argv) > 0 {
		sub = argv[0]
	}
	switch sub {
	case "start":
		if err := startDaemonRuntime(ctx, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "daemon failed: %v\n", err)
			return 1
		}
		return 0
	case "stop", "status":
		var code int
		var err error
		if sub == "stop" {
			code, err = daemon.DaemonStop(stdout)
		} else {
			code, err = daemon.DaemonStatus(stdout)
		}
		if err != nil {
			fmt.Fprintf(stderr, "daemon %s: %v\n", sub, err)
			return 1
		}
		return code
	default:
		fmt.Fprintln(stderr, "Usage: cave-tools daemon [start|stop|status]")
		return 2
	}
}

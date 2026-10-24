// Package bashstatus ports src/tools/bash-status.ts: the cave__bash_status
// tool.
package bashstatus

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	"github.com/groobybugs/cave-tools/go/internal/jsre"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// port of src/tools/bash-status.ts:DEFAULT_TAIL_LINES
const defaultTailLines = 50

// port of src/tools/bash-status.ts:MAX_WAIT_SECONDS
const maxWaitSeconds = 60

// port of src/tools/bash-status.ts:TAIL_READ_BYTES
const tailReadBytes = 64 * 1024

// whitespaceRunRE is the inline /\s+/g in truncateCommand.
var whitespaceRunRE = jsre.MustCompile(`\s+`, "g")

// Tool is the cave__bash_status MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__bash_status",
	Annotations: json.RawMessage(`{"readOnlyHint":true}`),
	Description: "Check a cave__bash_start job: state, exit code, duration, log output.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"jobId":{"type":"string","description":"Omit to list session jobs"},"tail_lines":{"type":"number","minimum":0,"description":"Log lines; 0 = status only","default":50},"cursor":{"type":"number","minimum":0,"description":"Prior next_cursor for incremental output; omit for tail"},"wait":{"type":"number","description":"Seconds to block until exit (max 60)"},"redact_secrets":{"type":"boolean","default":true}}}`),
	Handler:     handle,
}

// durationMs is (job.endedAt ?? Date.now()) - job.startedAt.
// port of src/tools/bash-status.ts:durationMs
func durationMs(job storage.JobRecord) int64 {
	end := time.Now().UnixMilli()
	if job.EndedAt != nil {
		end = *job.EndedAt
	}
	return end - job.StartedAt
}

// formatState renders a job's state line.
// port of src/tools/bash-status.ts:formatState
func formatState(job storage.JobRecord) string {
	// Math.round: halves go toward +Infinity, unlike math.Round.
	secs := int64(math.Floor(float64(durationMs(job))/1000 + 0.5))
	switch job.State {
	case storage.JobRunning:
		return "running (" + strconv.FormatInt(secs, 10) + "s elapsed, pid " + strconv.FormatInt(job.Pid, 10) + ")"
	case storage.JobExited:
		code := "?"
		if job.ExitCode != nil {
			code = strconv.FormatInt(*job.ExitCode, 10)
		}
		signal := ""
		if job.Signal != nil && *job.Signal != "" {
			signal = ", signal " + *job.Signal
		}
		return "exited (code " + code + signal + ") after " + strconv.FormatInt(secs, 10) + "s"
	case storage.JobKilled:
		signal := "SIGTERM"
		if job.Signal != nil {
			signal = *job.Signal
		}
		return "killed (" + signal + ") after " + strconv.FormatInt(secs, 10) + "s"
	case storage.JobLost:
		return "lost (server restarted; exit code unrecoverable — log tail below)"
	default:
		return "undefined"
	}
}

// truncateCommand flattens a command to one 120-column line.
// port of src/tools/bash-status.ts:truncateCommand
func truncateCommand(command string) string {
	oneLine := jsre.Trim(whitespaceRunRE.ReplaceAllString(command, " "))
	if utf16Length(oneLine) > 120 {
		return sliceUTF16(oneLine, 117) + "..."
	}
	return oneLine
}

// nonnegativeInteger is TS's nonnegativeInteger(value, fallback): undefined
// yields fallback, anything else must parse as a finite non-negative number
// and is floored. The second result is false where TS returns null.
// port of src/tools/bash-status.ts:nonnegativeInteger
func nonnegativeInteger(value any, defined bool, fallback int64) (int64, bool) {
	if !defined {
		return fallback, true
	}
	parsed := jsNumber(value)
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, false
	}
	if parsed >= math.MaxInt64 {
		return math.MaxInt64, true
	}
	return int64(math.Floor(parsed)), true
}

// limitIncrementalLines keeps at most maxLines complete lines. maxLines stays
// fractional like TS; the loop compares with `<` before any slice boundary.
// port of src/tools/bash-status.ts:limitIncrementalLines
func limitIncrementalLines(text string, maxLines float64) (string, int64, bool) {
	end := 0
	for line := 0; float64(line) < maxLines; line++ {
		index := strings.Index(text[end:], "\n")
		if index == -1 {
			return text, int64(len(text)), false
		}
		end += index + 1
	}
	if end >= len(text) {
		return text, int64(len(text)), false
	}
	limited := text[:end]
	return limited, int64(len(limited)), true
}

// utf16Length is JS string length (UTF-16 code units).
func utf16Length(value string) int {
	count := 0
	for _, char := range value {
		if char > 0xffff {
			count += 2
		} else {
			count++
		}
	}
	return count
}

// sliceUTF16 mirrors String.prototype.slice(0, end) over UTF-16 code units.
func sliceUTF16(value string, end int) string {
	units := utf16.Encode([]rune(value))
	if end < len(units) {
		units = units[:end]
	}
	return string(utf16.Decode(units))
}

// handle is the cave__bash_status handler.
// port of src/tools/bash-status.ts:bashStatusTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	sessionID := "default"
	if raw, ok := args["__sessionId"]; ok && raw != nil {
		sessionID = jsString(raw)
	}
	shouldRedact := !isFalse(args["redact_secrets"])

	jobIDRaw, hasJobID := args["jobId"]
	if !hasJobID {
		jobs, err := rt.ListSessionJobs(sessionID)
		if err != nil {
			return types.ToolResult{}, err
		}
		if len(jobs) == 0 {
			return textResult("No background jobs for this session."), nil
		}
		lines := make([]string, len(jobs))
		for i, job := range jobs {
			lines[i] = job.JobID + "  " + formatState(job) + "  " + truncateCommand(job.Command)
		}
		listing := strings.Join(lines, "\n")
		if shouldRedact {
			listing = compression.RedactSecrets(listing)
		}
		return textResult(compression.ApplyBudget(ctx, listing, "bash")), nil
	}

	// JSON null stays defined in JS (`args.jobId === undefined` is false), so
	// it becomes the string "null" rather than the listing branch.
	jobID := jsString(jobIDRaw)
	job, err := rt.GetJobInfo(jobID)
	if err != nil {
		return types.ToolResult{}, err
	}
	if job == nil {
		return errorResult("Unknown job: " + jobID), nil
	}

	if job.State == storage.JobRunning {
		if waitRaw, hasWait := args["wait"]; hasWait {
			waitSeconds := jsNumber(waitRaw)
			if math.IsNaN(waitSeconds) || waitSeconds == 0 {
				waitSeconds = 0
			}
			waitSeconds = math.Max(0, math.Min(maxWaitSeconds, waitSeconds))
			waited, err := rt.WaitForJob(jobID, int64(waitSeconds*1000))
			if err != nil {
				return types.ToolResult{}, err
			}
			if waited != nil {
				job = waited
			}
		}
	}

	tailLinesRaw, hasTailLines := args["tail_lines"]
	tailLines, tailOK := nonnegativeInteger(tailLinesRaw, hasTailLines, defaultTailLines)
	cursorRaw, hasCursor := args["cursor"]
	cursor, cursorOK := nonnegativeInteger(cursorRaw, hasCursor, 0)
	if !tailOK || !cursorOK {
		return errorResult("tail_lines and cursor must be non-negative numbers"), nil
	}

	header := []string{
		job.JobID + "  " + formatState(*job),
		"command: " + truncateCommand(job.Command),
		"log: " + job.LogPath,
	}

	var rawText string
	switch {
	case tailLines == 0:
		logSize := rt.FileByteSize(job.LogPath)
		rawText = strings.Join(append(append([]string{}, header...),
			"log_size: "+strconv.FormatInt(logSize, 10)+" bytes",
			"next_cursor: "+strconv.FormatInt(logSize, 10),
			"",
			"(log output omitted; tail_lines=0 read no log bytes; use cursor=0 to read existing output)",
		), "\n")
	case hasCursor:
		chunk := rt.ReadFileChunk(job.LogPath, cursor, tailReadBytes)
		budgetLines := math.Max(1, compression.GetBudget("bash").MaxLines-8)
		limitedText, consumedBytes, limited := limitIncrementalLines(chunk.Text, math.Min(float64(tailLines), budgetLines))
		nextCursor := chunk.StartCursor + consumedBytes
		hasMore := nextCursor < chunk.FileSize
		notes := make([]string, 0, 2)
		if chunk.CursorReset {
			notes = append(notes, "cursor_reset: "+strconv.FormatInt(chunk.RequestedCursor, 10)+
				" exceeded log size; resumed at 0")
		}
		if hasMore {
			reason := strconv.Itoa(int(tailReadBytes)) + "-byte read limit"
			if limited {
				reason = "line limit"
			}
			notes = append(notes, "output_truncated: more log bytes remain ("+reason+"); continue with cursor="+
				strconv.FormatInt(nextCursor, 10))
		}
		body := jsre.TrimEnd(limitedText)
		if body == "" {
			body = "(no new log output)"
		}
		lines := append([]string{}, header...)
		lines = append(lines,
			"cursor: "+strconv.FormatInt(chunk.StartCursor, 10)+"-"+strconv.FormatInt(nextCursor, 10)+
				" of "+strconv.FormatInt(chunk.FileSize, 10)+" bytes",
			"next_cursor: "+strconv.FormatInt(nextCursor, 10),
		)
		lines = append(lines, notes...)
		lines = append(lines, "", body)
		rawText = strings.Join(lines, "\n")
	default:
		logSize := rt.FileByteSize(job.LogPath)
		start := logSize - tailReadBytes
		if start < 0 {
			start = 0
		}
		chunk := rt.ReadFileChunk(job.LogPath, start, tailReadBytes)
		tail := chunk.Text
		lines := strings.Split(tail, "\n")
		lineTruncated := false
		if int64(len(lines)) > tailLines {
			tail = strings.Join(lines[len(lines)-int(tailLines):], "\n")
			lineTruncated = true
		}
		tailTruncated := chunk.StartCursor > 0 || lineTruncated
		body := jsre.TrimEnd(tail)
		if body == "" {
			body = "(log empty)"
		}
		out := append([]string{}, header...)
		out = append(out, "next_cursor: "+strconv.FormatInt(chunk.FileSize, 10))
		if tailTruncated {
			out = append(out, "tail_truncated: earlier log output omitted; use cursor=0 for incremental output")
		}
		out = append(out, "", body)
		rawText = strings.Join(out, "\n")
	}

	protectedText := rawText
	if shouldRedact {
		protectedText = compression.RedactSecrets(rawText)
	}
	text := compression.ApplyBudget(ctx, protectedText, "bash")
	handle := compression.ArchiveHandle(protectedText, text, "bash_status:"+job.JobID)

	return types.ToolResult{
		Content: []types.Content{types.Text(text + handle)},
		IsError: job.State == storage.JobExited && (job.ExitCode == nil || *job.ExitCode != 0),
	}, nil
}

func textResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

// isFalse reports whether value is the JSON boolean false (TS `=== false`).
func isFalse(value any) bool {
	boolean, ok := value.(bool)
	return ok && !boolean
}

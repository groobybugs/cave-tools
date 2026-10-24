// Package bashstart ports src/tools/bash-start.ts: the cave__bash_start tool.
package bashstart

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/groobybugs/cave-tools/go/internal/compression"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// Tool is the cave__bash_start MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__bash_start",
	Description: "Run a long command (builds, tests, installs) detached when its final output should be compressed; returns a jobId, output goes to a log file. Poll with cave__bash_status, stop with cave__bash_stop. Never sleep-poll. Servers/watchers: native background shell.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string","description":"5-10 word summary"},"workdir":{"type":"string","description":"Use instead of cd"}},"required":["command","description"]}`),
	Handler:     handle,
}

// handle is the cave__bash_start handler.
// port of src/tools/bash-start.ts:bashStartTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	command := jsArgString(args, "command")
	description := ""
	if raw, ok := args["description"]; ok && raw != nil {
		description = jsString(raw)
	}
	sessionID := "default"
	if raw, ok := args["__sessionId"]; ok && raw != nil {
		sessionID = jsString(raw)
	}

	var workdir *string
	if raw, ok := args["workdir"]; ok && jsTruthy(raw) {
		cwd, err := rt.ResolveExistingDirectory(ctx, jsString(raw))
		if err != nil {
			return types.ToolResult{
				Content: []types.Content{types.Text("Path not found or not a directory: " + jsString(raw))},
				IsError: true,
			}, nil
		}
		workdir = &cwd
	}

	job, err := rt.StartJob(ctx, rt.StartJobInput{
		Command:   command,
		SessionID: sessionID,
		Workdir:   workdir,
	})
	if err != nil {
		return types.ToolResult{
			Content: []types.Content{types.Text(compression.RedactSecrets("Failed to start background job: " + err.Error()))},
			IsError: true,
		}, nil
	}

	commandText := compression.RedactSecrets(command)
	title := description
	if title == "" {
		title = commandText
	}
	text := "Background job started: " + title + "\n" +
		"jobId: " + job.JobID + "\n" +
		"pid: " + strconv.FormatInt(job.Pid, 10) + "\n" +
		"logPath: " + job.LogPath + "\n" +
		"\n" +
		"Poll with cave__bash_status { \"jobId\": \"" + job.JobID + "\", \"wait\": 60 } — wait returns on exit. Use tail_lines=0 for status only, or reuse next_cursor for new output only. Stop with cave__bash_stop."
	return types.ToolResult{Content: []types.Content{types.Text(text)}}, nil
}

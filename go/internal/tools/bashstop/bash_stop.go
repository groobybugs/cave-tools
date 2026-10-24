// Package bashstop ports src/tools/bash-stop.ts: the cave__bash_stop tool.
package bashstop

import (
	"context"
	"encoding/json"
	"strconv"

	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// supportedSignals is the SIGNALS set; Set iteration order is insertion order.
// port of src/tools/bash-stop.ts:SIGNALS
var supportedSignals = []string{"SIGTERM", "SIGINT", "SIGKILL", "SIGHUP"}

func isSupportedSignal(name string) bool {
	for _, signal := range supportedSignals {
		if signal == name {
			return true
		}
	}
	return false
}

// Tool is the cave__bash_stop MCP definition.
var Tool = types.ToolDefinition{
	Name:        "cave__bash_stop",
	Description: "Stop a cave__bash_start job by signalling its process group; escalates to SIGKILL after 3s.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"jobId":{"type":"string"},"signal":{"type":"string","description":"SIGTERM, SIGINT, SIGHUP, or SIGKILL","default":"SIGTERM"}},"required":["jobId"]}`),
	Handler:     handle,
}

// handle is the cave__bash_stop handler.
// port of src/tools/bash-stop.ts:bashStopTool.handler
func handle(ctx context.Context, args map[string]any) (types.ToolResult, error) {
	jobID := jsArgString(args, "jobId")
	requested := "SIGTERM"
	if raw, ok := args["signal"]; ok && raw != nil {
		requested = jsString(raw)
	}
	if !isSupportedSignal(requested) {
		joined := ""
		for index, signal := range supportedSignals {
			if index > 0 {
				joined += ", "
			}
			joined += signal
		}
		return errorResult("Unsupported signal: " + requested + ". Use one of: " + joined), nil
	}

	job, err := rt.StopJob(jobID, requested)
	if err != nil {
		return types.ToolResult{}, err
	}
	if job == nil {
		return errorResult("Unknown job: " + jobID), nil
	}
	if job.State != "killed" {
		exit := ""
		if job.ExitCode != nil {
			exit = ", exit " + strconv.FormatInt(*job.ExitCode, 10)
		}
		return textResult(job.JobID + " already finished (state: " + string(job.State) + exit + ")."), nil
	}
	return textResult(job.JobID + " killed (sent " + requested + " to process group " +
		strconv.FormatInt(job.Pid, 10) + "; SIGKILL follows in 3s if needed). Log: " + job.LogPath), nil
}

func textResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}}
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{Content: []types.Content{types.Text(text)}, IsError: true}
}

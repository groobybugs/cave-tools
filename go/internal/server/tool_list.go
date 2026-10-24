package server

import (
	"github.com/groobybugs/cave-tools/go/internal/tools/applypatch"
	"github.com/groobybugs/cave-tools/go/internal/tools/bash"
	"github.com/groobybugs/cave-tools/go/internal/tools/bashstart"
	"github.com/groobybugs/cave-tools/go/internal/tools/bashstatus"
	"github.com/groobybugs/cave-tools/go/internal/tools/bashstop"
	"github.com/groobybugs/cave-tools/go/internal/tools/compress"
	"github.com/groobybugs/cave-tools/go/internal/tools/configure"
	"github.com/groobybugs/cave-tools/go/internal/tools/edit"
	"github.com/groobybugs/cave-tools/go/internal/tools/find"
	"github.com/groobybugs/cave-tools/go/internal/tools/grep"
	"github.com/groobybugs/cave-tools/go/internal/tools/invalidate"
	"github.com/groobybugs/cave-tools/go/internal/tools/ls"
	"github.com/groobybugs/cave-tools/go/internal/tools/read"
	"github.com/groobybugs/cave-tools/go/internal/tools/status"
	"github.com/groobybugs/cave-tools/go/internal/tools/webfetch"
	"github.com/groobybugs/cave-tools/go/internal/tools/websearch"
	"github.com/groobybugs/cave-tools/go/internal/tools/write"
	"github.com/groobybugs/cave-tools/go/internal/types"
)

// AllTools is every tool schema, in tools/list order. It mirrors TS's allTools
// array: the order is load-bearing because tools/list publishes it.
// port of src/tool-list.ts:allTools
var AllTools = []types.ToolDefinition{
	read.Tool,
	bash.Tool,
	bashstart.Tool,
	bashstatus.Tool,
	bashstop.Tool,
	grep.Tool,
	find.Tool,
	ls.Tool,
	write.Tool,
	invalidate.Tool,
	edit.Tool,
	applypatch.Tool,
	websearch.Tool,
	webfetch.Tool,
	compress.Tool,
	status.Tool,
	configure.Tool,
}

// ToolNames is the dispatch loader insertion order from TS. CallTool and the
// registry use it as the fixed tool-name allowlist.
// port of src/dispatch.ts:TOOL_NAMES
var ToolNames = []string{
	"cave__read",
	"cave__bash",
	"cave__bash_start",
	"cave__bash_status",
	"cave__bash_stop",
	"cave__grep",
	"cave__find",
	"cave__ls",
	"cave__write",
	"cave__invalidate",
	"cave__edit",
	"cave__apply_patch",
	"cave__websearch",
	"cave__webfetch",
	"cave__compress",
	"cave__status",
	"cave__configure",
}

// ListedTools returns the profile-filtered tool list.
// port of src/tool-list.ts:listedTools
func ListedTools(profile ...string) []types.ToolDefinition {
	return FilterByProfile(AllTools, func(tool types.ToolDefinition) string { return tool.Name }, profile...)
}

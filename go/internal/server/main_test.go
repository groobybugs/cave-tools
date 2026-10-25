package server

import (
	"os"
	"testing"

	"github.com/groobybugs/cave-tools/go/internal/testenv"
)

// TestMain keeps the in-process MCP server on the same neutral environment as
// the TS child it is compared with: a subagent-like key in only one side would
// change read freshness or add a stderr warning.
func TestMain(m *testing.M) {
	testenv.ScrubProcess()
	os.Exit(m.Run())
}

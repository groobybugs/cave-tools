// Minimal local reader for the persistent tool-error log.
// Read-only: never writes. Best-effort: an unreadable store prints zero
// records, never an error trace.
//
// port of src/runtime/tool-errors-cli.ts
package runtime

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// decimalOnlyRE is the TS /^\d+$/ integer check.
// port of src/runtime/tool-errors-cli.ts:parseLimit
var decimalOnlyRE = jsre.MustCompile(`^\d+$`, "")

// usage prints the command usage and reports the usage exit code.
// port of src/runtime/tool-errors-cli.ts:usage
func usage(stderr io.Writer) int {
	fmt.Fprintln(stderr, "Usage: cave-tools errors [--limit N]")
	return 2
}

// parseLimit parses [--limit N] or [--limit=N]; any other shape is a usage
// error. Integer only: fractional, negative, empty or non-numeric values fail.
// port of src/runtime/tool-errors-cli.ts:parseLimit
func parseLimit(argv []string, stderr io.Writer) (int, int, bool) {
	if len(argv) == 0 {
		return 20, 0, true
	}
	var value string
	haveValue := false
	if len(argv) == 2 && argv[0] == "--limit" {
		value = argv[1]
		haveValue = true
	} else if len(argv) == 1 && strings.HasPrefix(argv[0], "--limit=") {
		value = argv[0][len("--limit="):]
		haveValue = true
	} else {
		return 0, usage(stderr), false
	}
	if !haveValue || !decimalOnlyRE.MatchString(value) {
		return 0, usage(stderr), false
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n > 9007199254740991 || n < 1 {
		return 0, usage(stderr), false
	}
	if n > 500 {
		n = 500
	}
	return int(n), 0, true
}

// countEntry is one key/count pair from the aggregates.
type countEntry struct {
	key   string
	count int64
}

// sortedCounts orders count entries like TS's stable sort by count desc.
func sortedCounts(values map[string]int64, order []string) []countEntry {
	entries := make([]countEntry, 0, len(values))
	for _, key := range order {
		entries = append(entries, countEntry{key: key, count: values[key]})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
	return entries
}

// formatCounts renders `k=v k=v`.
func formatCounts(entries []countEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, entry.key+"="+strconv.FormatInt(entry.count, 10))
	}
	return strings.Join(parts, " ")
}

// isoMillis renders a JS Date.toISOString() value for a millisecond stamp.
func isoMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// RunErrorsCommand prints the log summary and recent records. Returns the
// process exit code (0, or 2 for usage errors).
// port of src/runtime/tool-errors-cli.ts:runErrorsCommand
func RunErrorsCommand(argv []string) int {
	return runErrorsCommandTo(argv, os.Stdout, os.Stderr)
}

// runErrorsCommandTo is RunErrorsCommand with explicit output streams.
func runErrorsCommandTo(argv []string, stdout, stderr io.Writer) int {
	limit, code, ok := parseLimit(argv, stderr)
	if !ok {
		return code
	}
	counts := CountToolErrors()
	fmt.Fprintf(stdout, "error log: %s\n", ToolErrorsDBPath())
	fmt.Fprintf(stdout, "total: %d\n", counts.Total)
	if counts.Total == 0 {
		fmt.Fprintln(stdout, "No tool errors recorded.")
		return 0
	}
	fmt.Fprintf(stdout, "by code: %s\n", formatCounts(sortedCounts(counts.ByCode, counts.byCodeOrder)))
	fmt.Fprintf(stdout, "by tool: %s\n", formatCounts(sortedCounts(counts.ByTool, counts.byToolOrder)))
	fmt.Fprintf(stdout, "recent (last %d):\n", limit)
	for _, record := range ListToolErrors(limit) {
		when := isoMillis(record.CreatedAt)
		op := ""
		if record.Operation != nil && *record.Operation != "" {
			op = " op=" + *record.Operation
		}
		batch := ""
		if record.BatchIndex != nil {
			batch = " batch=" + strconv.FormatInt(*record.BatchIndex, 10)
		}
		file := ""
		if record.FileHash != nil && *record.FileHash != "" {
			file = " file=" + *record.FileHash
		}
		kind := ""
		if record.ErrorKind != nil && *record.ErrorKind != "" {
			kind = " kind=" + *record.ErrorKind
		}
		args := ""
		if len(record.ArgKeys) > 0 || record.UnknownArgCount > 0 {
			args = " args=[" + strings.Join(record.ArgKeys, ",") + "]"
			if record.UnknownArgCount > 0 {
				args += " +" + strconv.FormatInt(record.UnknownArgCount, 10) + " unknown"
			}
		}
		fmt.Fprintf(stdout,
			"%s id=%s tool=%s outcome=%s code=%s elapsed=%dms mode=%s agent=%s%s%s session=%s%s%s%s\n",
			when,
			record.ID,
			record.Tool,
			string(record.Outcome),
			string(record.ReasonCode),
			record.ElapsedMs,
			record.Mode,
			record.Agent,
			op,
			batch,
			record.SessionHash,
			file,
			kind,
			args,
		)
	}
	return 0
}

# W5a parity findings

Environment: TypeScript dist built from `main` `6cefd81` plus the reviewed
opt-in switch (`a0dcc6e`); Go merged from `a0dcc6e`. All tests below live in
`go/parity/` behind the `parity` build tag and run with
`go test -tags parity ./parity/`.

## Resolved mismatches

All three W5a findings below are fixed and their parity cases are unchanged.
Items 1-2 landed on the sibling branch `groobybugs/go-w5-fix-a` and item 3 on
this branch, so this branch's parity run still reports the two cases owned by
the sibling (see each entry).

1. **Missing-path suggestions are unsorted in Go.** — **Fixed** in `68e8835`
   (branch `groobybugs/go-w5-fix-a`; not on this branch).
   TS `src/tools/read.ts:134` uses Node/`uv_fs_scandir` `readdir`, which sorts
   entries; Go `go/internal/tools/read/read.go:211` iterated
   `handle.ReadDir(-1)` in filesystem order and took the first three matches.
   Case: `read .txt` reports `crlf.txt, empty.txt, lines.txt` (TS) versus
   `unicode.txt, note.txt, nonl.txt` (Go).
   Test: `TestCLIBlackBoxDifferential/read.missing-multi-suggestion-order`.
   Single-match and no-match suggestions already matched.

2. **Empty `cave__bash` command succeeds in Go.** — **Fixed** in `9cabc7a`
   (branch `groobybugs/go-w5-fix-a`; not on this branch).
   TS `src/runtime/process.ts:234` calls `spawn(command, ...)`; Node throws
   `The argument 'file' cannot be empty` for an empty command, so the tool
   returns the `Command failed to launch` error (exit 1). Go
   `go/internal/tools/bash/bash.go` passed `""` to the shell, which exited 0
   with no output.
   Test: `TestCLIBlackBoxDifferential/bash.empty-command`.

3. **`filter --json` malformed-JSON wording differs.** — **Fixed** in
   `8b8adef` (V8 message text) and `db43715` (stdin decoding + number
   semantics).
   Go `go/internal/cli/cli_tools.go` surfaced `encoding/json` text
   (`invalid character 'o' in literal null (expecting 'u')`). TS
   `JSON.parse` on Node 24 says
   `Unexpected token 'o', "not json" is not valid JSON`. Exit code and empty
   stdout already match.
   Test: `TestCLIBlackBoxDifferential/filter.invalid-json`.

## W5b follow-up findings (fixed)

Review of `8b8adef` found two more `filter --json` stdin mismatches. Both are
fixed on this branch in `db43715`, with node-recorded expectations in
`1a64770` (`go/internal/cli/testdata/gen_filter_cases.mjs`).

1. **Invalid UTF-8 stdin decoded differently.** `readAllStdin` used
   `strings.ToValidUTF8`, which emits one U+FFFD per invalid *run*; Node's
   `setEncoding("utf8")` (StringDecoder/WHATWG) emits one per maximal invalid
   subpart. E.g. `ff fe 7b 7d` was `"\uFFFD{}"` (1 replacement) in Go versus
   `"\uFFFD\uFFFD{}"` in TS; `ed a0 80` was 1 versus 3 replacements and
   `c0 af` 1 versus 2. Go now reuses `runtime.DecodeUTF8Replace` (exported from
   `internal/runtime/process.go`, formerly `decodeUTF8Replace`), which was
   already the exact Node decoder used by `BoundedCapture`. Both stdin callers
   (`runToolCommand` `-` values and `filter --json`) go through it, matching the
   single TS `readAllStdin`. Fixture `testdata/decoder_cases.json` records
   StringDecoder output for whole buffers, every two-chunk split and one-byte
   chunks, so chunk boundaries provably do not matter and EOF-truncated
   sequences print exactly as Node prints them.

2. **float64 overflow rejected stdin.** V8 `JSON.parse` accepts `1e400`,
   `1e309`, `-1e400` and 400-digit integers as ±Infinity; `encoding/json` into
   `float64` errored, so Go exited 1 where TS exited 0. `filter --json` now
   decodes with `json.Decoder.UseNumber` and converts tokens via
   `strconv.ParseFloat`, treating `ErrRange` as signed Infinity (underflow
   `1e-400` -> 0, `-0` preserved) while keeping `JSON.parse`'s single-value
   (trailing-data) strictness. `exitCode` still accepts any number, including
   ±Infinity, and stays functionally ignored by the pipeline like TS.
   Fixture `filter_cases.json` covers extra fields, `exitCode`, nested values, arrays (`[1e400]`
   keeps TS's shape-error text), fractional/negative/huge exit codes,
   underflow, `-0` and trailing data.

## W5b follow-up: float64 overflow at the remaining `JSON.parse` sites (fixed)

Out-of-range number literals are accepted by TS `JSON.parse` (V8 yields
±Infinity, underflow yields ±0) but were rejected by Go's `json.Unmarshal`
into `float64`/`any`. The filter stdin site was fixed in `db43715`; the four
sites reported there are now fixed on this branch. The decoder moved to
`go/internal/jsjson/parse.go` as `jsjson.Parse` (same code, plus
`jsjson.StringifyValue` for JSON.stringify's `null`), and the CLI filter uses
it too, so there is one implementation.

1. **CLI array/object flag coercion.** `coerceFlagValue`
   (`go/internal/cli/cli_tools.go`) now parses with `jsjson.Parse`; number
   flags were already correct (`--offset 1e400` rejects with
   `--offset expects a number, got "1e400"` in both). Node-recorded vectors in
   `internal/cli/testdata/overflow_cases.json` (generated by
   `gen_overflow_cases.mjs`) via `TestCLIOverflowNumbersMatchNode`:
   - `edit --edits '[1e400]'`: TS parses `[Infinity]` then rejects the item
     (`Error: edits[0]: must be an edit object`, exit 1); Go previously exited 2
     with `--edits expects a JSON array`.
   - `edit --edits '[{"file_path":"…","start_line":1e400,…}]'`: TS reaches
     `Error: Cannot read file: …` (exit 1); Go previously rejected the flag.
   - `invalidate --file_paths '[1e400]'`: TS maps `String(Infinity)` to path
     `Infinity` and reports one file; output is count-only, so this case guards
     the coercion path rather than an observable path value.
   - stdin values (`--edits -`) go through the same coercion and are covered.
   - No tool in `dist` exposes an object-typed CLI property (only arrays), so
     the `case "object"` branch has no end-to-end vector; it uses the same
     helper.

2. **Daemon request args.** Two directions, matching TS exactly:
   - Raw wire with `1e400` in `args`: TS parses NDJSON with `JSON.parse`, keeps
     Infinity, and runs the tool. Go `decodeArgs` (`internal/daemon/server.go`)
     now uses `jsjson.Parse`; `1e400` no longer drops every argument.
   - Client serialization: TS writes requests with `JSON.stringify`, so
     Infinity/NaN become `null`. Go `DaemonClient.Call` now writes
     `jsjson.StringifyValue(args)`, which copies the args map and turns
     non-finite numbers into `null` (the caller's map is untouched so the
     in-process fallback still sees Infinity, like TS).
   Test: `TestServerArgsNumberOverflow` in `internal/daemon/server_test.go`
   (raw client sees `+Inf`; the Go client writes `null`).
   - Reply parsing needs no change: daemon replies are `JSON.stringify` output
     (never non-finite) and unknown fields are skipped by `encoding/json`.
     `internal/daemon/client.go` decodes only content blocks.

3. **MCP `tools/call` arguments.** The drop was in our code, not the SDK:
   `go-sdk v1.8.0` exposes `mcp.CallToolParamsRaw.Arguments` as
   `json.RawMessage`, so the raw JSON reaches `decodeCallArguments`
   (`internal/server/index.go`) before any typed decode. It now uses
   `jsjson.Parse`; the "File not found: undefined" empty-args fallback is
   gone. Tests: `TestDecodeCallArgumentsNumberOverflow`,
   `TestMCPCallArgumentsNumberOverflow`, and the node-recorded
   `TestMCPNumericOverflowCasesMatchNode`
   (`internal/server/testdata/gen_overflow_mcp_cases.mjs`). No SDK fork or
   patch was needed.

4. **Websearch provider response.** `parsePayload`
   (`internal/tools/websearch/websearch.go`) now uses `jsjson.Parse`, so a
   provider body like
   `{"result":{"content":[{"type":"text","text":"ok"}],"usage":{"count":1e400}}}`
   parses like TS instead of returning "no payload".
   Test: `TestParseWebsearchResponseNumberOverflow`.

### Infinity flow through numeric tool fields (verified TS vs Go)

With `arguments` carrying `1e400`, node and Go agree on every numeric schema
field checked (node fixture `overflow_mcp_cases.json`, replayed by
`TestMCPNumericOverflowCasesMatchNode`; daemon-enabled MCP was checked
separately):

| field | TS/Go result |
| --- | --- |
| `cave__read` `offset:1e400` | `Offset Infinity is out of range` (isError) |
| `cave__read` `offset:-1e400` | whole file (negative/`-Inf` clamps like TS `Math.max(1, …)`) |
| `cave__read` `limit:1e400` | whole file (slice end `Infinity` keeps everything) |
| `cave__bash` `timeout:1e400` | `Invalid timeout value: Infinity. Timeout must be a non-negative number.` |
| `cave__edit` `start_line:1e400` | `Error: start_line and end_line must be integers for range mode` |
| `cave__ls` `offset:1e400` | `(empty directory)` |
| `cave__grep` `limit:1e400` / `context:1e400` | full match list / full context, like TS |
| `cave__find` `limit:1e400` | all matches |
| `cave__bash_status` `tail_lines/wait:1e400` | same "no jobs" path (non-finite never reaches a slice) |
| `cave__configure` numeric fields | unchanged error path (`Unknown action: undefined` without `action`) |

Normal numbers keep their existing behaviour (fixtures include
`read --offset 2` and `start_line:2`). CLI number flags still reject Infinity
before the tool (`expects a number, got "1e400"`), as TS does.

### Internal state readers (deliberately unchanged)

`internal/storage/db.go`, `internal/runtime/jobs.go`,
`internal/daemon/paths.go`, `internal/runtime/tool_errors.go` and
`internal/status-ui` parse JSON emitted by this project, which never writes
non-finite numbers (`JSON.stringify(Infinity)` is `null`), so no concrete
differing input is known there.

## Daemon read-dedup smoke investigation (no defect found)

The benchmark observation (`go-w5-bench/scripts/bench-ts-go.md`) was
reproduced from its two halves; it is daemon-key/socket sharing, not a
per-session isolation defect:

- `TestDaemonReadSessionIsolation` runs all four daemon/client pairs
  (Go/Go, Go/TS, TS/Go, TS/TS) with private temp data/socket/config trees:
  session A first read full, a fresh session B also full, only A's second read
  is the `cli:A` stub. All pass.
- `TestDaemonSeparateSocketsSameDataKeepCachesIndependent` starts a TS daemon
  and a Go daemon with the **same data dir** but distinct short socket dirs:
  both first reads for the same explicit session are full. Read dedup is
  in-memory per daemon; the data dir is not a shared cache. Passes.
- `TestDaemonSharedSocketSessionNamespace` intentionally shares one daemon:
  TS session A full, Go session B full, Go A stub, TS B stub, and the same
  session C is shared across clients (`cli:C` stub) by design. Passes.

The earlier smoke-side mismatch had TS full and Go `stub` for the **same**
validation session, which is what happens when both clients reach one daemon.
A different-session stub was not reproducible: every daemon/client pair
returned a full read for fresh session B (see the four-case test above).
The current harness keys (TS `2bdf…`, Go with `CAVE_TOOLS_GO_DIST_DIR` `6788…`)
differ, so the fixed harness does not share a socket. One test-harness detail
matters: long temp paths push the socket path over `sun_path`, and both sides
then fall back to the shared `/tmp/cave-tools-<uid>` directory. The new daemon
tests use short runtime dirs so they exercise real separate sockets.

## Deliberately not asserted as failures

- Tool-error storage is documented best-effort: both implementations drop a
  queued record when SQLite is busy. `errors.seeded-aggregate` uses fixed
  seeded rows for exact aggregation/formatting; `errors.live-single` proves a
  single real failure is persisted. Rapid multi-process logging is not used as
  a gate because the documented busy-drop is nondeterministic (Go lost 2/30,
  TS 0/30 on this machine).
- Accepted deviations remain accepted and are not normalized into the suite:
  Brotli/stacked `Content-Encoding`, ICU/`localeCompare` ordering outside the
  covered fixtures, lone-surrogate JSON, unknown MCP content-block types, and
  malformed `tools/call` `params.name` protocol text.

## Not exercised (reported, not waived)

- `websearch`/`webfetch` success paths need network; only usage errors and the
  non-http scheme rejection (no I/O) are covered.
- `bins` download path: only the fake-rg/fd fixture is exercised; no download.
- Windows/MSYS shell behavior and named-pipe daemon transport: POSIX-only cases
  skip on Windows; Windows is covered by `GOOS=windows go vet`.
- Daemon malformed reply lines, idle in-flight shutdown, retry/backoff and
  request-drop-never-reruns are covered by existing package tests
  (`internal/daemon`), not duplicated as black-box cases here.
- `bash_start` output itself is nondeterministic (job id/time); its command
  mapping and durable behavior are covered by the cross-implementation job
  tests rather than a byte-for-byte argv row.

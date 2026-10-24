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

### Reported, not fixed: other Go `json.Unmarshal` sites vs `JSON.parse`

Out-of-range number literals are accepted by TS `JSON.parse` but rejected by
Go when unmarshalling into `float64`/`any`. Sites found outside this fix:

- `go/internal/cli/cli_tools.go:275,286` `coerceFlagValue` (array/object CLI
  flags, including values read from stdin with `-`). Concrete: TS
  `cave-tools invalidate --paths '[1e400]'` yields `[Infinity]` and the tool
  sees path `Infinity`; Go's `json.Unmarshal` fails and the string-items
  fallback passes `"[1e400]"` as the single path.
- `go/internal/server/index.go:194` (`tools/call` `arguments`). Concrete:
  `{"arguments":{"x":1e400}}` reaches the TS handler as `Infinity`; Go's
  unmarshal error produces an empty args object.
- `go/internal/daemon/server.go:421` + `decodeArgs` (`:532`) on request args.
  Concrete: a daemon request whose `args` contains `1e400` keeps the value in
  TS (`JSON.parse(line)`) and drops every arg in Go (`json.Unmarshal` into
  `map[string]any` fails).
- `go/internal/tools/websearch/websearch.go:296` parses the provider response
  where TS uses `JSON.parse`; a response containing `1e400` would be accepted
  by TS and rejected by Go.
- Internal state readers (`internal/storage/db.go`, `internal/runtime/jobs.go`,
  `internal/daemon/paths.go`, `internal/runtime/tool_errors.go`,
  `internal/status-ui`) parse JSON emitted by this project, which never writes
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

# W5a parity findings

Environment: TypeScript dist built from `main` `6cefd81` plus the reviewed
opt-in switch (`a0dcc6e`); Go merged from `a0dcc6e`. All tests below live in
`go/parity/` behind the `parity` build tag and run with
`go test -tags parity ./parity/`.

## Real mismatches still open

1. **Missing-path suggestions are unsorted in Go.**
   TS `src/tools/read.ts:134` uses Node/`uv_fs_scandir` `readdir`, which sorts
   entries; Go `go/internal/tools/read/read.go:211` iterates
   `handle.ReadDir(-1)` in filesystem order and takes the first three matches.
   Case: `read .txt` reports `crlf.txt, empty.txt, lines.txt` (TS) versus
   `unicode.txt, note.txt, nonl.txt` (Go).
   Test: `TestCLIBlackBoxDifferential/read.missing-multi-suggestion-order`.
   Single-match and no-match suggestions already match.

2. **Empty `cave__bash` command succeeds in Go.**
   TS `src/runtime/process.ts:234` calls `spawn(command, ...)`; Node throws
   `The argument 'file' cannot be empty` for an empty command, so the tool
   returns the `Command failed to launch` error (exit 1). Go
   `go/internal/tools/bash/bash.go` passes `""` to the shell, which exits 0 with
   no output.
   Test: `TestCLIBlackBoxDifferential/bash.empty-command`.

3. **`filter --json` malformed-JSON wording differs.**
   Go `go/internal/cli/cli_tools.go:974` surfaces `encoding/json` text
   (`invalid character 'o' in literal null (expecting 'u')`). TS
   `JSON.parse` on Node 24 says
   `Unexpected token 'o', "not json" is not valid JSON`. Exit code and empty
   stdout already match.
   Test: `TestCLIBlackBoxDifferential/filter.invalid-json`.

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

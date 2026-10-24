# W5a parity findings

Environment: TypeScript dist built from `main` `6cefd81` (merged into this
branch), Go binary stamped with `package.json` version `0.6.1`. All tests below
are in `go/parity/` behind the `parity` build tag and run with
`go test -tags parity ./parity/`.

## Blocking: main `6cefd81` mirror not merged

`migration/w5-sync` commit `51158ed` contains both mirrors; they are not merged
into this branch yet.

1. `cave__bash` description: Go still has the pre-`6cefd81` text. TS changed
   `src/tools/bash.ts` (native-shell-first wording). Go source:
   `go/internal/tools/bash/bash.go:31`.
   Failing tests: `TestCLIEveryToolHelp/bash`,
   `TestCLIEveryToolExplicitHelp/bash`, all four
   `TestMCPToolsListDifferential/*`, `TestParity/help`, `TestParity/help-bash`,
   `TestCLIBlackBoxDifferential/help.top-level`.
2. `init`/`install-agent` instruction block: Go still has the old
   `CAVE_INSTRUCTIONS` text (`go/internal/cli/cli.go`). TS changed
   `src/cli.ts`. Failing tests:
   `TestCLIBlackBoxDifferential/init.fresh-then-existing` and
   `init.existing-file`.

Both are byte-identical to TS in `51158ed` (verified by comparing the
description string and the native-shell guidance line), so merging that sync
commit should clear these failures without test changes.

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

## Deliberately not asserted as failures

- Tool-error storage is documented best-effort: both implementations drop a
  queued record when SQLite is busy (`flushOneBatch`/`StoreBusy`). The
  errors.flow case pauses 100 ms between failing CLI processes instead of
  manufacturing a lock race; format, count, codes, session hash and argument
  keys are still asserted exactly. A 1-in-5 loss was observed before adding the
  pause.
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

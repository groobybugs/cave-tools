# cave-tools Go port — porting contract

The TypeScript implementation in `src/` is the reference and keeps working.
The Go port lives in `go/` and is ported function by function, file by file.
Both implementations must be interchangeable: same output bytes, same error
texts, same exit codes, same files on disk, same SQLite schema and data dir.

The user must be able to switch between the two implementations easily. The
switch is **not implemented yet** (W4); the plan is at the end of this document.

## Layout

```
go/
  go.mod                        module github.com/groobybugs/cave-tools/go, go 1.26
  cmd/cave-tools-go/main.go     binary entry point (thin: argv -> internal/cli)
  internal/types/               shared MCP result shapes
  internal/runtime/             per-call context, processes, paths, bins, jobs
  internal/storage/             data dir + SQLite (stats, archives, jobs)
  internal/compression/         output compression + archiving
  internal/jsre/                JS regex + whitespace semantics for ported code
  internal/tools/               one file per MCP tool
  internal/server/              MCP stdio server, dispatch, tool list, profile
  internal/daemon/              daemon server, client, control, socket paths
  internal/cli/                 CLI dispatch, CLI-only tools, status UI
  parity/                       TS <-> Go parity harness (build tag `parity`)
```

Build and check:

```sh
cd go && go build ./... && go vet ./... && go test ./...
pnpm build && cd go && go test -tags parity ./parity/
pnpm build && pnpm test        # TS reference must stay green
```

Version stamping: `cmd/cave-tools-go/main.go` declares
`var version = "0.6.1"` and the build may override it with

```sh
go build -ldflags "-X main.version=$(node -p "require('./package.json').version")" ./cmd/cave-tools-go
```

Plain `go build` uses the constant. The parity harness always stamps the
package.json version, so a drift between the constant and package.json fails
`go test -tags parity`.

The binary is **never** named `cave-tools`: it is `cave-tools-go` so it cannot
shadow the TS launcher.

## TS → Go file map

One TS file maps to exactly one Go file with the same base name (kebab-case →
snake_case). Packages mirror the TS directories; the handful of top-level TS
files live in the package that owns their responsibility (`server` for the MCP
entry layer, `cli` for the CLI, `daemon` for the socket client).

| TS file | Go file | Wave |
| --- | --- | --- |
| `src/types.ts` | `go/internal/types/types.go` | W0 |
| `src/runtime/context.ts` | `go/internal/runtime/context.go` | W0 |
| `src/storage/data-dir.ts` | `go/internal/storage/data_dir.go` | W0 |
| `src/storage/db.ts` | `go/internal/storage/db.go` | W0 |
| `src/compression/aggressive.ts` | `go/internal/compression/aggressive.go` | W1a |
| `src/compression/classify.ts` | `go/internal/compression/classify.go` | W1a |
| `src/compression/codebook.ts` | `go/internal/compression/codebook.go` | W1a |
| `src/compression/diff.ts` | `go/internal/compression/diff.go` | W1a |
| `src/compression/redact.ts` | `go/internal/compression/redact.go` | W1a |
| `src/compression/repetition.ts` | `go/internal/compression/repetition.go` | W1a |
| `src/compression/signatures.ts` | `go/internal/compression/signatures.go` | W1a |
| `src/compression/snapshots.ts` | `go/internal/compression/snapshots.go` | W1a |
| `src/runtime/path.ts` | `go/internal/runtime/path.go` | W1b |
| `src/runtime/process.ts` | `go/internal/runtime/process.go` | W1b |
| `src/runtime/bins.ts` | `go/internal/runtime/bins.go` | W1b |
| `src/runtime/ripgrep.ts` | `go/internal/runtime/ripgrep.go` | W1b |
| `src/runtime/file-mutation.ts` | `go/internal/runtime/file_mutation.go` | W1b |
| `src/runtime/read-registry.ts` | `go/internal/runtime/read_registry.go` | W1b |
| `src/runtime/jobs.ts` | `go/internal/runtime/jobs.go` | W1c |
| `src/runtime/job-worker.ts` | `go/internal/runtime/job_worker.go` | W1c |
| `src/compression/archive.ts` | `go/internal/compression/archive.go` | W1c |
| `src/compression/utils.ts` | `go/internal/compression/utils.go` | W2d |
| `src/compression/structured.ts` | `go/internal/compression/structured.go` | W2d |
| `src/tools/read.ts` | `go/internal/tools/read.go` | W2e |
| `src/tools/line-range.ts` | `go/internal/tools/line_range.go` | W2e |
| `src/tools/edit.ts` | `go/internal/tools/edit.go` | W2f |
| `src/tools/edit-match.ts` | `go/internal/tools/edit_match.go` | W2f |
| `src/tools/replacers.ts` | `go/internal/tools/replacers.go` | W2f |
| `src/tools/write.ts` | `go/internal/tools/write.go` | W2f |
| `src/tools/apply-patch.ts` | `go/internal/tools/apply_patch.go` | W2f |
| `src/tools/bash.ts` | `go/internal/tools/bash.go` | W2g |
| `src/tools/bash-start.ts` | `go/internal/tools/bash_start.go` | W2g |
| `src/tools/bash-status.ts` | `go/internal/tools/bash_status.go` | W2g |
| `src/tools/bash-stop.ts` | `go/internal/tools/bash_stop.go` | W2g |
| `src/tools/grep.ts` | `go/internal/tools/grep.go` | W2g |
| `src/tools/find.ts` | `go/internal/tools/find.go` | W2g |
| `src/tools/ls.ts` | `go/internal/tools/ls.go` | W2g |
| `src/tools/invalidate.ts` | `go/internal/tools/invalidate.go` | W2g |
| `src/tools/webfetch.ts` | `go/internal/tools/webfetch.go` | W2h |
| `src/tools/websearch.ts` | `go/internal/tools/websearch.go` | W2h |
| `src/tools/compress.ts` | `go/internal/tools/compress.go` | W2h |
| `src/tools/status.ts` | `go/internal/tools/status.go` | W2h |
| `src/tools/configure.ts` | `go/internal/tools/configure.go` | W2h |
| `src/runtime/tool-errors.ts` | `go/internal/runtime/tool_errors.go` | W2h |
| `src/runtime/tool-errors-cli.ts` | `go/internal/runtime/tool_errors_cli.go` | W2h |
| `src/index.ts` | `go/internal/server/index.go` | W3i |
| `src/dispatch.ts` | `go/internal/server/dispatch.go` | W3i |
| `src/tool-list.ts` | `go/internal/server/tool_list.go` | W3i |
| `src/profile.ts` | `go/internal/server/profile.go` | W3i |
| `src/daemon/server.ts` | `go/internal/daemon/server.go` | W3j |
| `src/daemon/control.ts` | `go/internal/daemon/control.go` | W3j |
| `src/daemon/paths.ts` | `go/internal/daemon/paths.go` | W3j |
| `src/daemon-client.ts` | `go/internal/daemon/daemon_client.go` | W3j |
| `src/cli.ts` | `go/internal/cli/cli.go` | W3k |
| `src/cli-tools.ts` | `go/internal/cli/cli_tools.go` | W3k |
| `src/status-ui.ts` | `go/internal/cli/status_ui.go` | W3k |

`src/cli.ts` maps to `go/internal/cli/cli.go`, not to the binary: `cmd/cave-tools-go/main.go`
is the OS entry point and only forwards `os.Args[1:]`, stdout and stderr to
`cli.Run`. That keeps the TS file ↔ Go file pairing exact and makes the CLI
testable without spawning a process.

Non-Go files under `src/` (`src/plugins/**`) are not ported.

`go/internal/jsre` has no TS counterpart: it is the helper that makes ported
regexes and whitespace helpers behave like JavaScript (see
"Regex and whitespace rule" below).

### Durable job worker

TS starts a background job by spawning
`process.execPath dist/runtime/job-worker.js <jobId> <logPath> <resultPath> <shell> <command>`.
Go has no separate worker script, so it re-executes its own binary and uses
argv[1] as the worker selector; the remaining arguments keep TS's positions:

```
cave-tools-go __job-worker <jobId> <logPath> <resultPath> <shell> <command>
```

`runtime.JobWorkerCommand` is the selector (`"__job-worker"`). `jobs.StartJob`
passes it as `StartDurableBackgroundOptions.WorkerPath` to
`runtime.StartDurableBackgroundCommand`, which invokes the current executable
with `WorkerPath` first. `cmd/cave-tools-go/main.go` must check for it before
any flag parsing (W3k wiring):

```go
if len(os.Args) > 1 && os.Args[1] == runtime.JobWorkerCommand {
    os.Exit(runtime.RunJobWorker(os.Args[2:]))
}
```

The worker writes the same log header, `[cave-job exit=N signal=SIG]` marker
and `<jobId>.result.json` sidecar as TS, so both implementations can adopt
each other's running jobs.

## Package boundaries

```
types     <- nobody (leaf)
runtime   <- types
storage   <- nobody (stdlib + modernc.org/sqlite only; data-dir is SQLite-free)
jsre      <- nobody (leaf; stdlib only)
compression <- types, runtime, storage, jsre
tools     <- types, runtime, storage, compression
server    <- types, runtime, storage, compression, tools
daemon    <- types, runtime, storage, compression, tools
cli       <- everything (entry layer)
cmd/cave-tools-go <- cli
```

Rules:

- No import cycles. If two packages need the same helper, the lower one owns it.
- `server`, `daemon` and `cli` are entry layers: nothing imports them.
- `storage` must not import `runtime`/`compression`; it only reads env vars and
  the filesystem (matching `data-dir.ts`'s "kept free of SQLite" split, with
  `data_dir.go` importing no SQLite at all).
- Don't create a package that the map above does not list. If a port needs
  something from a package that does not exist yet, declare the smallest
  interface in the importing package and report it.

## Naming

- Exported TS functions become exported Go functions; TS-private helpers stay
  unexported. Same boundaries, same names, except Go initialisms:
  `dbPath` → `DBPath`, `getClientId` → `GetClientID`, `readRegistryFile` →
  `ReadRegistryFile`.
- One-line provenance comment above every ported declaration:
  `// port of src/storage/db.ts:openDb`.
- TS `interface`/`type` → Go `struct` or named type with the same field names
  in Go style and **JSON tags identical to the TS property names** (camelCase).
  The tags are load-bearing: legacy JSON files and the `*_json` columns are
  shared with TS.
- Optional TS fields become pointers (`maxChars?: number` → `*int64`), TS
  `number` becomes `int64` unless the value is a rate (`hitRate` → `float64`),
  TS `T | null` becomes `*T`.
- Optional parameters with defaults: variadic (`markSessionEnded(pid, endedAt?)`
  → `MarkSessionEnded(pid int64, endedAtMillis ...int64)`) or an explicit
  argument when a variadic bool would be unreadable (`listSessionStats(true)` →
  `ListSessionStats(includeEnded bool)`).
- Go files are `snake_case.go`; Go tests are `snake_case_test.go` in the same
  package (white-box), because several assertions read unexported state such as
  the DB singleton.

## Regex and whitespace rule

ECMAScript and RE2 disagree on the two things the compression code depends on.
Every regex ported from TS **must** be compiled through `go/internal/jsre`, and
every JS `trim`/`split(/\s+/)` **must** use its helpers:

```go
var re = jsre.MustCompile(`^ssh\s`, "i") // the original TS source + TS flags
trimmed := jsre.Trim(command)            // not strings.TrimSpace
parts := jsre.SplitWS(line)              // not strings.Fields
```

- Pass the TS regex source verbatim (including `\/` and escapes) and the TS
  flags as the second argument; `jsre` translates before compiling, so a
  reviewer can diff the Go literal against `src/` by eye. Never call
  `regexp.Compile`/`regexp.MustCompile` on a TS pattern, and never re-implement
  `\s` by hand at a call site.
- Supported flags: `g` (no-op: use `FindAll*`/`ReplaceAll*`), `i`, `m`, `s`,
  `u` (no-op for pattern syntax). Anything else is an error instead of being
  silently ignored.
- `jsre` rejects constructs RE2 cannot express — lookbehind, lookahead,
  backreferences, named groups, octal escapes, unknown escapes — with an error
  naming the construct. Port such a regex by hand and add an equivalence test
  on the TS test inputs (see the porting rules).

| JS construct | RE2 alone | `jsre` translation |
| --- | --- | --- |
| `\s` | ASCII only | ECMAScript WhiteSpace + LineTerminator: `\t\n\v\f\r`, space, U+00A0, U+1680, U+2000–U+200A, U+2028, U+2029, U+202F, U+205F, U+3000, U+FEFF |
| `\S` | ASCII only | explicit complement of that set (also inside classes, including negated ones) |
| `.` | excludes only `\n` | excludes `\n`, `\r`, U+2028, U+2029; `(?s:.)` with the `s` flag |
| `[]` / `[^]` | invalid | never matches / matches every character |
| `\uXXXX`, `\xXX`, `\u{...}` | `\uXXXX` unsupported | `\x{...}` |
| `\b` inside a class | invalid | `\x{8}` (backspace) |

Whitespace helpers (JS semantics, not `unicode.IsSpace`):

- `jsre.IsSpace(r)` is exactly ECMAScript WhiteSpace + LineTerminator. It
  includes U+FEFF and excludes U+0085, the opposite of `unicode.IsSpace`.
- `jsre.Trim` / `TrimStart` / `TrimEnd` mirror the `String.prototype` methods.
- `jsre.SplitWS(s)` mirrors `s.split(/\s+/)`: a whitespace run is one
  separator and the empty string at either end is kept (`" a"` → `["", "a"]`),
  which is why `normalizeLine` filters empties after splitting instead of using
  `strings.Fields`.

Known limitation: `i` maps to RE2's Unicode case folding, slightly wider than
JavaScript's non-`u` Canonicalize (ſ and the Kelvin sign fold to s/k in Go).
TS only applies `i` to ASCII command words, so no shipped pattern can observe
it. The other unmatchable corner — global iteration over a pattern that can
match the empty string — is pinned by
`go/internal/jsre.TestGlobalEmptyMatchIteration`; every ported pattern needs at
least one character (`+`, `{n,}` or a literal), so it does not arise.

## Context flow

`src/runtime/context.ts` uses `AsyncLocalStorage`; Go threads it explicitly:

| TS | Go |
| --- | --- |
| `runWithContext(ctx, fn)` | `runtime.RunWithContext(ctx, call, fn)` (returns the derived `context.Context`) |
| `getContext()` | `runtime.GetContext(ctx)` |
| `getCwd()` | `runtime.GetCwd(ctx)` |
| `getEnv()` | `runtime.GetEnv(ctx)` |
| `getClientId()` | `runtime.GetClientID(ctx)` |
| `localContext(sessionId)` | `runtime.LocalContext(sessionID)` |

Rules for every ported function that reads cwd/env/session: take
`ctx context.Context` as the first parameter and read through the `runtime`
helpers. Never call `os.Getenv`/`os.Getwd` directly in `tools`,
`compression`, `storage` or `daemon` code; the daemon serves each client with
its own cwd/env and only the context helpers honour that. The exceptions are
the module-level env reads that TS also does outside a call
(`CAVE_TOOLS_DATA_DIR`, `CLAUDE_CONFIG_DIR`) — those live in `storage`.

`CallContext.Env` is a `map[string]string` (Node's `ProcessEnv` may hold
`undefined` values; Go omits them). Use `runtime.EnvFromEnviron(os.Environ())`
when building one.

## Errors and ToolResult flow

- `types.ToolResult` mirrors `src/types.ts`: `Content []Content`, `IsError
  bool`, `Meta map[string]any`. Build blocks with `types.Text`, `types.Image`,
  `types.ResourceBlock` so the `type` discriminator is always right.
- Expected tool failures (bad argument, missing file, command exited non-zero)
  are results with `IsError: true` and a text block, exactly like TS. They are
  not Go errors.
- Unexpected failures (programmer error, DB failure) are Go `error` values whose
  message text matches the TS exception message (`fmt.Errorf("%s", msg)`), so
  the CLI's stderr and exit code stay identical. Do not invent new wording and
  do not wrap with extra prefix text that TS does not print.
- CLI exit codes are part of the contract: `0` success, `2` usage errors,
  `1` runtime errors — copy `src/cli.ts` and `src/cli-tools.ts` exactly.
- `error` is never swallowed where TS would throw. The two deliberate swallow
  sites are ported as-is: `registerReadPath` (advisory registry) and the
  per-file guards in the legacy migrations.

## Shared state with TS

Both implementations share `CAVE_TOOLS_DATA_DIR` (default
`~/.cache/cave-tools`), the `cave-tools.db` file, `archives/`, `jobs/`,
`sessions/`, `lifetime.json` and `$CLAUDE_CONFIG_DIR/cave-tools/read-registry.txt`.

- Schema text, table/index names, column order and defaults are byte-identical
  to `src/storage/db.ts`; `user_version` stays untouched (TS never sets it) and
  the `legacy_migration_v1` meta marker is the migration guard.
- Pragmas: `journal_mode = WAL`, `synchronous = NORMAL`, `busy_timeout = 5000`.
  TS applies them once after open; Go applies them through the driver DSN so
  every pooled connection gets them. Observable DB state is the same.
- Permissions: data dir, `jobs/`, `archives/` are tightened to `0700`; the db
  and its `-wal`/`-shm` sidecars to `0600`, including pre-existing loose files.
  Go pre-creates the db `0600` so SQLite inherits it, like TS.
- Legacy JSON (`lifetime.json`, `sessions/*.json`, `archive_meta` sidecars,
  `read-registry.txt`) is parsed with the TS property names. Keep the JSON tags
  in sync if a TS shape changes.

## Test conventions

- Port the TS assertions that cover your files (`test.mjs` and
  `scripts/test-*.mjs`) into `<file>_test.go` next to your file. Table-driven,
  one subtest name per TS assertion, same expected strings/bytes.
- Tests must be hermetic: `t.Setenv("CAVE_TOOLS_DATA_DIR", t.TempDir())`,
  `t.Setenv("CLAUDE_CONFIG_DIR", ...)` and never touch `~/.cache/cave-tools`,
  `~/.claude` or the real repo files. Tests that change env or the storage
  singleton must not run in parallel.
- The storage singleton is process-wide like TS's module-level `db`; tests
  reset it through the unexported `db` variable (`resetDB` helper in
  `internal/storage/db_test.go`).
- Interop is mandatory for storage: `go/internal/storage/interop_test.go`
  writes with Go and reads with `dist/storage/db.js`, then the reverse. It skips
  with a clear message when `dist/` is missing.
- Parity cases live in `go/parity/parity_test.go` (build tag `parity`, run with
  `go test -tags parity ./parity/`). Add a case for every CLI-reachable
  behaviour; cases are `{name, args, stdin, env}` and compare stdout, stderr
  and exit code against `node dist/cli.js`. `knownDivergence` exists only for
  the wave-0 stub and must be deleted when the port catches up.
- Done means: `cd go && go vet ./... && go test ./...` green, parity green, and
  `pnpm build && pnpm test` still green.

## Waves

- W0 foundation (this task): go.mod, types, runtime/context, storage/data-dir + db, parity harness, contract doc.
- W1 (parallel):
  - a: compression leaves — aggressive, classify, codebook, diff, redact, repetition, signatures, snapshots
  - b: runtime — path, process, bins, ripgrep, file-mutation, read-registry
  - c: runtime jobs + job-worker; compression/archive
- W2 (parallel):
  - d: compression/utils + structured
  - e: tools read + line-range
  - f: tools edit + edit-match + replacers + write + apply-patch
  - g: tools bash + bash-start + bash-status + bash-stop + grep + find + ls + invalidate
  - h: tools webfetch + websearch + compress + status + configure; runtime/tool-errors + tool-errors-cli
- W3 (parallel):
  - i: dispatch + tool-list + profile + index (MCP stdio server via go-sdk)
  - j: daemon server/client/control/paths
  - k: CLI — cli, cli-tools, status-ui (incl. raw, filter --json)
- W4: integration — full parity suite, `CAVE_TOOLS_IMPL=go` switch in the launcher, docs.

### Ownership per worker

| Worker | Owns |
| --- | --- |
| W0 | `go/` (all of it, this wave only), `docs/go-port.md` |
| W1a | `go/internal/compression/{aggressive,classify,codebook,diff,redact,repetition,signatures,snapshots}.go` + tests |
| W1a-jsre | `go/internal/jsre/` (new), the eight W1a compression files + tests, `docs/go-port.md` |
| W1b | `go/internal/runtime/{path,process,bins,ripgrep,file_mutation,read_registry}.go` + tests |
| W1c | `go/internal/runtime/{jobs,job_worker}.go`, `go/internal/compression/archive.go` + tests |
| W2d | `go/internal/compression/{utils,structured}.go` + tests |
| W2e | `go/internal/tools/{read,line_range}.go` + tests |
| W2f | `go/internal/tools/{edit,edit_match,replacers,write,apply_patch}.go` + tests |
| W2g | `go/internal/tools/{bash,bash_start,bash_status,bash_stop,grep,find,ls,invalidate}.go` + tests |
| W2h | `go/internal/tools/{webfetch,websearch,compress,status,configure}.go`, `go/internal/runtime/{tool_errors,tool_errors_cli}.go` + tests |
| W3i | `go/internal/server/*.go` + tests |
| W3j | `go/internal/daemon/*.go` + tests |
| W3k | `go/internal/cli/*.go`, `go/cmd/cave-tools-go/main.go` + tests |
| W4 | integration, launcher switch, full parity suite |

`go.mod` is W0-only: later waves must not add dependencies. If one is truly
needed, ask the coordinator.

## Easy switch (planned for W4, not implemented)

Default stays TypeScript.

1. Build the Go binary next to the TS dist, never named `cave-tools`:
   `cd go && go build -o bin/cave-tools-go ./cmd/cave-tools-go`.
2. `src/cli.ts` gains a shim at the very top of the file, before any heavy
   import: if `process.env.CAVE_TOOLS_IMPL === "go"`, resolve the Go binary
   (`CAVE_TOOLS_GO_BIN` if set, else `go/bin/cave-tools-go` relative to the
   package root) and re-exec it with the same argv, stdio inherited, propagating
   the exit code and signals. Everything else — `mcp`, `status`, `raw`,
   `filter`, daemon — is covered by that single re-exec, so the switch lives in
   one place.
3. Unset or any other value keeps the current TS behaviour, so `pnpm test` and
   existing installs are untouched.
4. Because both implementations share `CAVE_TOOLS_DATA_DIR` and the SQLite
   schema, switching mid-session loses no stats, archives or jobs. The daemon
   is per implementation: stop the TS daemon before switching
   (`cave-tools daemon stop`) because the socket/protocol handshake is not
   cross-compatible.
5. W4 adds parity cases for the CLI surface and a test that runs the same
   command with `CAVE_TOOLS_IMPL=go` and without it, diffing stdout/stderr/exit
   code.

## Known deviations from TS (all in W0 storage)

1. `budgets_json` key order: Go's `encoding/json` sorts map keys, TS's
   `JSON.stringify` preserves insertion order. The JSON is semantically equal
   and both sides parse it; no consumer depends on order.
2. `isError` is omitted when false (Go `omitempty`). TS never sets it to false,
   so the emitted results are identical.
3. `GetCwd` returns `""` when the process cwd no longer exists; TS
   `process.cwd()` throws in that case.
4. `ListSessionStats(bool)` and `MarkSessionEnded(pid, ...)` make TS's default
   parameters explicit/variadic; behaviour is unchanged.

Accepted legacy-migration deviations (one-time import of pre-SQLite files; not worth statement-by-statement emulation):

- A legacy session with `budgets: null` is stored as `{}` (TS stores `null`).
- A legacy session missing `savings.byTool` or `bounces.byExtension` keeps its row with no tool/bounce rows in Go; TS keeps the row and throws before the tool/bounce rows.
- String-typed numbers in legacy `lifetime.json` (e.g. `"calls": "5"`) read as 0 in Go; TS lets SQLite coerce them to 5.

Accepted runtime deviations (W1b, Muse review):

- Fallback file finder (no rg/fd) sorts byte-wise; TS uses `localeCompare` (ICU), so mixed-case order can differ.
- `RunCommand` options `MaxBytes`/`MaxLines`/`ForceKillAfterMs` treat 0 as "use default"; TS `??` keeps an explicit 0. No TS caller passes 0.
- `BinResolution` uses `""` for a missing binary where TS uses `null`. W3k: `cave-tools bins` must print `(missing)` for `""` (TS prints it for null).
- Child output is decoded once from the full byte buffer; TS decodes per chunk, so a multibyte char split across chunks becomes U+FFFD in TS but decodes correctly in Go.

- Job `.result.json` with a fractional `exitCode` (only reachable by hand-editing the sidecar) is truncated to an integer in Go; TS keeps the fraction.

### TS bugs found while porting

- `src/storage/db.ts:595` (and the `try/catch` at `src/storage/db.ts:405-414`):
  `JSON.stringify(stats.budgets)` is `undefined` when a legacy `sessions/*.json`
  file predates the `budgets` field. better-sqlite3 binds that as `NULL` and
  `budgets_json TEXT NOT NULL` rejects the row, so `migrateSessions` silently
  skips the whole session (same for a missing `bounces`/`savings` object).
  Ported faithfully: Go skips the same legacy files (`legacySessionAcceptedByTS`).
- `src/storage/db.ts:806` `updateJobState` overwrites `ended_at`, `exit_code`
  and `signal` with `NULL` whenever the caller omits them (`update.x ?? null`
  is intentional per call site, but it means a state-only call such as
  `updateJobState(id, { state: "lost" })` erases a recorded exit code). Go
  reproduces it exactly; flagging it so the fix lands in TS first if wanted.

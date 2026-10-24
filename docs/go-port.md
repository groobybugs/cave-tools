# cave-tools Go port — porting contract

The TypeScript implementation in `src/` is the reference and keeps working.
The Go port lives in `go/` and is ported function by function, file by file.
Both implementations are designed to be interchangeable: same output bytes,
same error texts, same exit codes, same files on disk, same SQLite schema and
data dir. Interchangeability is a per-behaviour claim backed by tests, not a
blanket statement about every function; see "Verification status" and "Known
deviations and limitations".

## Status

- W0–W4 are integrated on `go-port` at `02890a1` (proof-coercion fix
  `fa5d968` included). Every one of the 56 `src/**/*.ts` files listed below
  has a mapped Go implementation on this branch.
- Source truth for the TypeScript reference is `main` at `6cefd81`. The Go
  branch has not merged that commit yet; the W5 sync is a separate, active
  task. Do not read the file map below as "main 6cefd81 is fully mirrored".
- W5 (main sync, parity, implementation switch, benchmark, docs) is in
  progress in separate worktrees. Nothing in this document claims those
  changes are merged or tested unless it names a test and result.
- TypeScript remains the default implementation. `CAVE_TOOLS_IMPL=go` is
  **not integrated yet**; see "Implementations switch".

## Layout

```
go/
  go.mod                        module github.com/groobybugs/cave-tools/go, go 1.26
  cmd/cave-tools-go/main.go     binary entry point (thin: argv -> internal/cli)
  internal/types/               shared MCP result shapes, ToolDefinition
  internal/runtime/             per-call context, processes, paths, bins, jobs
  internal/storage/             data dir + SQLite (stats, archives, jobs)
  internal/compression/         output compression + archiving
  internal/jsre/                JS regex + whitespace semantics for ported code
  internal/tools/               shared tool helpers (line range, edit match, replacers)
  internal/tools/<tool>/        one package per MCP tool (17 packages)
  internal/server/              MCP stdio server, dispatch, tool list, profile
  internal/daemon/              daemon server, client, control, socket paths
  internal/cli/                 CLI dispatch, CLI-only tools, status UI
  parity/                       TS <-> Go parity harness (build tag `parity`)
```

The 17 tool packages are `applypatch`, `bash`, `bashstart`, `bashstatus`,
`bashstop`, `compress`, `configure`, `edit`, `find`, `grep`, `invalidate`,
`ls`, `read`, `status`, `webfetch`, `websearch` and `write`. Each exports
`var Tool types.ToolDefinition` from its main file; package names drop dashes
(`bash-start.ts` → `bashstart/bash_start.go`).

Build and check:

```sh
cd go && go build ./... && go vet ./... && go test -count=1 ./...
GOOS=windows go vet ./... && GOOS=darwin go vet ./...
pnpm build && cd go && go test -tags parity ./parity/
pnpm build && pnpm test        # TS reference must stay green
```

Build the local binary next to the TS tree, never named `cave-tools`:

```sh
cd go && go build -o bin/cave-tools-go ./cmd/cave-tools-go
```

Version stamping: `cmd/cave-tools-go/main.go` declares
`var version = "0.6.1"` and `go build` may override it with

```sh
VERSION=$(node -p "require('./package.json').version")
go build -ldflags "-X main.version=$VERSION" -o bin/cave-tools-go ./cmd/cave-tools-go
```

Plain `go build` uses the constant. The parity harness rebuilds the Go binary
with the `package.json` version stamped in, so it compares against that value;
it does not fail merely because the plain-build constant drifts. `--version` is
an intentional Go-only probe (TS has no `--version` branch) and is the single
`knownDivergence` in the parity table. The binary is **never** named
`cave-tools`: it is `cave-tools-go` so it cannot shadow the TS launcher.

## TS → Go file map

One TS file maps to exactly one Go file with the same base name (kebab-case →
snake_case). Packages mirror the TS directories; the handful of top-level TS
files live in the package that owns their responsibility (`server` for the MCP
entry layer, `cli` for the CLI, `daemon` for the socket client).

The wave column is the wave that actually ported the file, not an early plan:
W2e is the shared tool helpers, W2k is status/profile/daemon paths+control,
W3a–e are the tools, W4i/j/k are the surfaces.

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
| `src/tools/read.ts` | `go/internal/tools/read/read.go` | W3a |
| `src/tools/line-range.ts` | `go/internal/tools/line_range.go` | W2e |
| `src/tools/edit.ts` | `go/internal/tools/edit/edit.go` | W3b |
| `src/tools/edit-match.ts` | `go/internal/tools/edit_match.go` | W2e |
| `src/tools/replacers.ts` | `go/internal/tools/replacers.go` | W2e |
| `src/tools/write.ts` | `go/internal/tools/write/write.go` | W3b |
| `src/tools/apply-patch.ts` | `go/internal/tools/applypatch/apply_patch.go` | W3b |
| `src/tools/bash.ts` | `go/internal/tools/bash/bash.go` | W3c |
| `src/tools/bash-start.ts` | `go/internal/tools/bashstart/bash_start.go` | W3c |
| `src/tools/bash-status.ts` | `go/internal/tools/bashstatus/bash_status.go` | W3c |
| `src/tools/bash-stop.ts` | `go/internal/tools/bashstop/bash_stop.go` | W3c |
| `src/tools/grep.ts` | `go/internal/tools/grep/grep.go` | W3c |
| `src/tools/find.ts` | `go/internal/tools/find/find.go` | W3c |
| `src/tools/ls.ts` | `go/internal/tools/ls/ls.go` | W3c |
| `src/tools/invalidate.ts` | `go/internal/tools/invalidate/invalidate.go` | W3c |
| `src/tools/webfetch.ts` | `go/internal/tools/webfetch/webfetch.go` | W3d |
| `src/tools/websearch.ts` | `go/internal/tools/websearch/websearch.go` | W3d |
| `src/tools/compress.ts` | `go/internal/tools/compress/compress.go` | W3d |
| `src/tools/status.ts` | `go/internal/tools/status/status.go` | W3e |
| `src/tools/configure.ts` | `go/internal/tools/configure/configure.go` | W3e |
| `src/runtime/tool-errors.ts` | `go/internal/runtime/tool_errors.go` | W3e |
| `src/runtime/tool-errors-cli.ts` | `go/internal/runtime/tool_errors_cli.go` | W3e |
| `src/index.ts` | `go/internal/server/index.go` | W4i |
| `src/dispatch.ts` | `go/internal/server/dispatch.go` | W4i |
| `src/tool-list.ts` | `go/internal/server/tool_list.go` | W4i |
| `src/profile.ts` | `go/internal/server/profile.go` | W2k |
| `src/daemon/server.ts` | `go/internal/daemon/server.go` | W4j |
| `src/daemon/control.ts` | `go/internal/daemon/control.go` | W2k |
| `src/daemon/paths.ts` | `go/internal/daemon/paths.go` | W2k |
| `src/daemon-client.ts` | `go/internal/daemon/client.go` | W4j |
| `src/cli.ts` | `go/internal/cli/cli.go` | W4k |
| `src/cli-tools.ts` | `go/internal/cli/cli_tools.go` | W4k |
| `src/status-ui.ts` | `go/internal/cli/status_ui.go` | W2k |

All 56 rows were checked against the tree at `02890a1`: every TS file exists,
every Go target exists, and no `src/**/*.ts` file is missing from the table.
The map itself is not proof of function coverage; see "Coverage and annotation
audit".

`go/internal/cli` additionally holds helpers that belong to the two CLI files
above rather than separate TS modules: `status_summary.go` (the
status/statusline half of `cli.ts`), `cli_raw.go` and `cli_platform_*.go` (raw
shell execution and Win32 `cmd.exe` quoting), `integration.go` and
`bindings_{server,daemon}.go` (the server/daemon seams `cli-tools.ts` reaches
through its lazy imports).

`src/cli.ts` maps to `go/internal/cli/cli.go`, not to the binary:
`cmd/cave-tools-go/main.go` is the OS entry point and only forwards
`os.Args[1:]`, stdin, stdout and stderr to `cli.Run`. That keeps the TS file ↔
Go file pairing exact and makes the CLI testable without spawning a process.

### Non-TS adapter files

`src/plugins/**` is intentionally not ported. These files adapt external agents
to the CLI/MCP surface and are orthogonal to the implementation switch:

- `src/plugins/opencode/index.js`, `src/plugins/opencode/plugin.js`,
  `src/plugins/opencode/package.json`, `src/plugins/opencode/commands/cave-tools.md`
- `src/plugins/hermes/__init__.py`, `src/plugins/hermes/plugin.yaml`

`go/internal/jsre` also has no single TS counterpart: it is the helper that
makes ported regexes and whitespace helpers behave like JavaScript (see
"Regex and whitespace rule").

### Go-only glue

These files have no one-to-one TS file. They are process wiring, JavaScript
semantics helpers or platform splits, not extra product features:

| Go-only path | Purpose |
| --- | --- |
| `go/cmd/cave-tools-go/main.go` | argv/stdio/version wiring and the `__job-worker` selector. |
| `go/internal/types/tool.go` | `ToolDefinition` shape shared by every tool package and the server. |
| `go/internal/jsre/jsre.go` | ECMAScript regex/whitespace translation. |
| `go/internal/runtime/cmdline.go`, `go/internal/runtime/locale.go` | Node `cmd.exe` command-line construction; ICU-like collation for names (printable ASCII). |
| `go/internal/runtime/process_unix.go`, `process_windows.go`, `bins_unix.go`, `bins_windows.go`, `tool_errors_unix.go`, `tool_errors_windows.go` | OS branches mirrored from TS `process.platform`. |
| `go/internal/compression/utils_unix.go`, `utils_windows.go` | OS-specific path/volume helpers. |
| `go/internal/daemon/paths_unix.go`, `paths_windows.go`, `transport_unix.go`, `transport_windows.go` | Unix socket vs Windows named-pipe transport and socket-directory rules. |
| `go/internal/cli/status_summary.go`, `cli_raw.go`, `cli_platform_unix.go`, `cli_platform_windows.go`, `integration.go`, `bindings_server.go`, `bindings_daemon.go` | status/statusline, raw shell, Win32 quoting, and the CLI ↔ server/daemon seams. |
| `args.go` in the `bash`, `bashstart`, `bashstatus`, `bashstop`, `find`, `grep`, `invalidate` and `ls` tool packages | Shared JS argument coercion for those handlers. |
| `go/internal/tools/bash/signal_unix.go`, `go/internal/tools/bash/signal_windows.go` | Signal naming/exit-code mapping. |
| `go/internal/tools/read/jsutil.go`, `go/internal/tools/read/path.go`, `go/internal/tools/read/path_unix.go`, `go/internal/tools/read/path_windows.go` | JS `String`/`Number`/locale and Node path semantics used by `read.ts`. |
| `go/parity/` | TS ↔ Go parity harness (no TS module). |
| `go/internal/tools/webfetch/known-gaps.md` | Brotli/stacked-encoding limitation record. |

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
with `WorkerPath` first. `cmd/cave-tools-go/main.go` checks for it before any
flag parsing and calls `runtime.RunJobWorker(os.Args[2:])`.

The worker writes the same log header, `[cave-job exit=N signal=SIG]` marker
and `<jobId>.result.json` sidecar as TS, so both implementations can adopt
each other's running jobs.

## Package boundaries

```
types        <- nobody (leaf)
jsre         <- nobody (leaf; stdlib only)
storage      <- jsre
runtime      <- types, storage, jsre
compression  <- types, runtime, storage, jsre
tools        <- types, runtime, jsre          (shared helpers only)
tools/<tool> <- types, runtime, compression, jsre, shared tools (some add storage)
server       <- types, runtime, compression, daemon, tools/<tool>
daemon       <- types, runtime, storage, compression, jsre
cli          <- everything (entry layer; wiring through hooks/bindings)
cmd          <- cli, runtime
```

Rules:

- No import cycles. If two packages need the same helper, the lower one owns it.
- `daemon` must never import `server` or a tool package. It receives the tool
  registry and dispatch callback from its caller (`StartDaemon`, below), which
  is what breaks the cycle.
- `server` imports the daemon client to make daemon-first calls; `cli` imports
  both and wires them in `bindings_server.go` / `bindings_daemon.go`. Only
  `cmd/cave-tools-go` is a true process entry point; "entry layer" for
  `server`, `daemon` and `cli` means no lower package imports them, not that
  they cannot import each other in this direction.
- `storage` must not import `runtime`/`compression`; it only reads env vars and
  the filesystem (matching `data-dir.ts`'s "kept free of SQLite" split, with
  `data_dir.go` importing no SQLite at all).
- Don't create a package that the map above does not list. If a port needs
  something from a package that does not exist yet, declare the smallest
  interface in the importing package and report it.

### Daemon callback and shared protocol

`daemon.StartDaemon` accepts the registry and dispatch function rather than
importing the server:

```go
func StartDaemon(
    ctx context.Context,
    tools []types.ToolDefinition,
    callTool func(context.Context, string, map[string]any, *runtime.CallContext) (types.ToolResult, error),
) error
```

`cli/bindings_server.go` supplies `server.AllTools` and `server.CallTool`;
tests inject fixture tools and callbacks. The daemon serves NDJSON methods
`hello`, `list`, `call` and `shutdown` over the shared socket name.

Stable exported API used across packages:

- `server`: `ToolNames`, `AllTools`, `IsToolName`, `LoadTool`, `CallTool`,
  `ListedTools`, `StartServer(ctx, version, in, out, errout)`.
- `daemon`: `HelloOptions`, `CallOutcome{Sent,Result}`,
  `DaemonEnabled(env ...map[string]string)`, `GetDaemonClient`,
  `(*DaemonClient).Call`, `(*DaemonClient).List`, `(*DaemonClient).Close`,
  `ConnectionLostText`, `StartDaemon`.

Socket/protocol interop is a requirement, not an option:

- Both implementations derive the same 12-hex-character daemon key from
  `buildId` (package version + every compiled `.js` name/size/mtime),
  `CAVE_TOOLS_DATA_DIR` and `CLAUDE_CONFIG_DIR`; the Unix socket is
  `$XDG_RUNTIME_DIR/cave-tools/daemon-<key>.sock` (or
  `<dataDir>/run/daemon-<key>.sock`; owner-only temp dir when the path would
  exceed `sun_path`), and the Windows named pipe is
  `\\.\pipe\cave-tools-<key>`.
- The Go client locates the repository `dist/` (walking up from the executable
  and cwd, or via `CAVE_TOOLS_GO_DIST_DIR` passed to a re-executed daemon), so
  it derives the same key as a TS daemon. A standalone Go binary outside the
  repository with neither path available cannot derive the key and stays
  in-process; that is recorded under limitations.
- `daemon/client_test.go` has both directions: `TestClientToTSDaemon` runs a
  real TS daemon and calls it with the Go client; `TestTSClientToGoDaemon`
  runs the Go daemon and calls it with `dist/daemon-client.js`. Both skip when
  `node` or `dist/` is missing, and both skip on Windows.
- Because the socket, key and NDJSON protocol are shared, switching the CLI
  implementation does not require stopping a running daemon that was built
  from the same `dist/`. After either side is rebuilt the build id changes and
  the existing reconnect/spawn behavior applies; stopping the daemon is not a
  protocol requirement.

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
- Optional TS fields become pointers (`maxChars?: number` → `*float64`), TS
  `number` becomes `int64` unless the value is a rate (`hitRate` → `float64`),
  TS `T | null` becomes `*T`. Budget values are the deliberate exception:
  `BudgetConfig` stores them as `float64` so fractional budgets survive
  (see "Review fixes worth keeping").
- Optional parameters with defaults: variadic
  (`markSessionEnded(pid, endedAt?)` → `MarkSessionEnded(pid int64, endedAtMillis ...int64)`)
  or an explicit argument when a variadic bool would be unreadable
  (`listSessionStats(true)` → `ListSessionStats(includeEnded bool)`).
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
- The same rule covers JS regexes that come from Node rather than from `src/`
  (for example the cmd.exe detection in `process_windows.go`) and patterns
  assembled at runtime (for example `globToRegExp` in `ripgrep.go`). Only
  Go-only regexes with no JS origin may use the `regexp` package directly, and
  they must be named in the worker's report.
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

- Schema text, table/index names, column order and defaults follow
  `src/storage/db.ts`; tests pin table/index names and the key `sessions`
  columns. `user_version` stays untouched (TS never sets it) and the
  `legacy_migration_v1` meta marker is the migration guard.
- Pragmas: `journal_mode = WAL`, `synchronous = NORMAL`, `busy_timeout = 5000`.
  TS applies them once after open; Go applies them through the driver DSN so
  every pooled connection gets them. Observable DB state is the same.
- Permissions: data dir, `jobs/`, `archives/` are tightened to `0700`; the db
  and its `-wal`/`-shm` sidecars to `0600`, including pre-existing loose files.
  Go pre-creates the db `0600` so SQLite inherits it, like TS.
- Legacy JSON (`lifetime.json`, `sessions/*.json`, `archive_meta` sidecars,
  `read-registry.txt`) is parsed with the TS property names. Keep the JSON tags
  in sync if a TS shape changes.
- Interop is mandatory for storage: `go/internal/storage/interop_test.go`
  writes with Go and reads with `dist/storage/db.js`, then the reverse. It
  skips when `dist/` is missing.

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
- Parity cases live in `go/parity/parity_test.go` and
  `go/parity/integration_test.go` (build tag `parity`, run with
  `go test -tags parity ./parity/`). Add a case for every CLI-reachable
  behaviour; cases compare stdout, stderr and exit code against
  `node dist/cli.js`. `knownDivergence` exists only for the intentional
  `--version` Go probe; delete a divergence entry when the two sides agree.
- Differential tests (edit/write/apply-patch, webfetch, websearch, storage,
  daemon) call the built TS handlers from `dist/` and skip when `node` or
  `dist/` is absent. They are the strongest per-behaviour evidence, but each
  covers the cases it lists, not every function.
- Done means: `cd go && go vet ./... && go test ./...` green, parity green, and
  `pnpm build && pnpm test` still green.

## Waves

The waves below are the ones that actually ran; early plan labels were folded
into W3. W5 is not finished.

- **W0 foundation**: `go.mod`, `types`, `runtime/context`, `storage/data-dir`,
  `storage/db`, the parity harness and this contract.
- **W1 leaves + runtime**:
  - W1a: compression leaves (`aggressive`, `classify`, `codebook`, `diff`,
    `redact`, `repetition`, `signatures`, `snapshots`) plus `jsre`.
  - W1b: `runtime/{path,process,bins,ripgrep,file-mutation,read-registry}`.
  - W1c: `runtime/{jobs,job-worker}` and `compression/archive`.
- **W2 utilities + independent helpers**:
  - W2d: `compression/{utils,structured}`.
  - W2e: shared tool helpers `tools/{line_range,edit_match,replacers}.go`.
  - W2k: `status-ui`, `profile`, `daemon/{paths,control}`.
- **W3 tools**:
  - W3a: `read`.
  - W3b: `edit`, `write`, `apply-patch`.
  - W3c: `bash`, `bash-start`, `bash-status`, `bash-stop`, `grep`, `find`,
    `ls`, `invalidate`.
  - W3d: `webfetch`, `websearch`, `compress`.
  - W3e: `status`, `configure`, `tool-errors`, `tool-errors-cli`.
- **W4 surfaces**:
  - W4i: MCP server (`index`, `dispatch`, `tool-list`) and dispatch integration.
  - W4j: daemon server/client plus transport.
  - W4k: CLI (`cli`, `cli-tools`) and the `cave-tools-go` executable wiring.
- **W5 integration (in progress)**: sync to `main` `6cefd81`, remaining parity,
  `CAVE_TOOLS_IMPL=go` switch, benchmark harness, final docs/audit. These are
  separate worktrees; this document does not claim they are merged.

### Ownership per worker

| Worker | Owns (paths or package directories) |
| --- | --- |
| W0 | `go/` scaffold (`go.mod`, types, runtime/context, storage, parity), `docs/go-port.md` |
| W1a | `go/internal/jsre/`, compression leaves in `go/internal/compression/aggressive.go`, `classify.go`, `codebook.go`, `diff.go`, `redact.go`, `repetition.go`, `signatures.go`, `snapshots.go` + tests |
| W1b | `go/internal/runtime/path.go`, `process.go`, `bins.go`, `ripgrep.go`, `file_mutation.go`, `read_registry.go` + platform/tests |
| W1c | `go/internal/runtime/jobs.go`, `job_worker.go`, `go/internal/compression/archive.go` + tests |
| W2d | `go/internal/compression/utils.go`, `structured.go` + tests |
| W2e | `go/internal/tools/line_range.go`, `edit_match.go`, `replacers.go` + tests |
| W2k | `go/internal/cli/status_ui.go`, `go/internal/server/profile.go`, `go/internal/daemon/paths*.go`, `go/internal/daemon/control*.go` + tests |
| W3a | `go/internal/tools/read/` |
| W3b | `go/internal/tools/edit/`, `go/internal/tools/write/`, `go/internal/tools/applypatch/` |
| W3c | `go/internal/tools/bash/`, `bashstart/`, `bashstatus/`, `bashstop/`, `grep/`, `find/`, `ls/`, `invalidate/` |
| W3d | `go/internal/tools/webfetch/`, `websearch/`, `compress/` |
| W3e | `go/internal/tools/status/`, `configure/`, `go/internal/runtime/tool_errors*.go` |
| W4i | `go/internal/server/index.go`, `dispatch.go`, `tool_list.go` + tests |
| W4j | `go/internal/daemon/server.go`, `client.go`, `paths_*.go`, `transport_*.go` + tests |
| W4k | `go/internal/cli/`, `go/cmd/cave-tools-go/` + tests |
| W5 | parity expansion, launcher switch, benchmark, sync, docs/audit |

`go.mod` is W0-only: later waves must not add dependencies. If one is truly
needed, ask the coordinator.

## Implementations switch (W5, pending)

Default stays TypeScript. The switch is **not integrated** at `02890a1`, so
there is no tested `CAVE_TOOLS_IMPL=go` path yet. Planned contract:

1. `src/cli.ts` gets a shim before heavy imports: when
   `process.env.CAVE_TOOLS_IMPL === "go"`, resolve the Go binary
   (`CAVE_TOOLS_GO_BIN` if set, else `go/bin/cave-tools-go` relative to the
   package root) and re-exec it with the same argv, stdio inherited,
   propagating the exit code and signals. `mcp`, `status`, `raw`, `filter`,
   daemon and tool subcommands are all covered by that one re-exec.
2. A configured-but-missing binary fails with a clear non-zero error instead
   of silently falling back to TS. The exact error text is part of the W5
   switch change and must be tested there.
3. Unset, empty or any other value keeps the current TS behaviour, so
   `pnpm test` and existing installs are untouched.
4. No install, no dependency and no PATH change: the user builds
   `go/bin/cave-tools-go` locally. The binary is never named `cave-tools`.
5. Both implementations share the data dir, SQLite schema, daemon key, socket
   and NDJSON protocol. Switching does not require stopping a running daemon
   built from the same `dist/`; the existing reconnect/spawn behavior applies
   after a rebuild.
6. W5 adds a CLI parity case that runs the same command with
   `CAVE_TOOLS_IMPL=go` and without it, diffing stdout/stderr/exit code, plus
   the missing-binary/error-path test. Until that lands and is run, no switch
   claim is valid.

## Review fixes worth keeping

These are implemented behaviour, not deviations; they are easy to regress when
touching budgets or status:

- `storage.BudgetConfig` stores TS numbers as `float64`, so `max_chars: 12.7`
  survives configure/status. Non-finite values serialize as `null`, and
  `maxChars` stays omitted when TS holds `undefined`.
- `truncateLines`/`truncateChars` keep fractional line/char budgets and coerce
  to integers only inside slice operations, matching `ToIntegerOrInfinity`.
- Status ordering follows TS `Object.entries` (integer-like keys ascending,
  then insertion order) and per-extension `Map` order for bounce ties.
- `TestHandlerParity` for `cave__status` runs in a fresh test-binary process
  with a temp data dir, so the storage singleton cannot make it pass
  incidentally.

## Known deviations and limitations

These are verified against the code and tests at `02890a1`. They are **not**
blanket parity claims and none of them is labelled "accepted parity"; each
needs a follow-up decision or stays a documented limit.

### Storage and serialization

1. `budgets_json` map key order: Go's `encoding/json` sorts map keys; TS's
   `JSON.stringify` preserves insertion order. The JSON is semantically equal
   and both sides parse it; no consumer depends on order.
2. `isError` is omitted when false (Go `omitempty`). TS never sets it to false,
   so emitted results are identical.
3. `GetCwd` returns `""` when the process cwd no longer exists; TS
   `process.cwd()` throws in that case.
4. `ListSessionStats(bool)` and `MarkSessionEnded(pid, ...)` make TS's default
   parameters explicit/variadic; behaviour is unchanged.
5. Legacy `lifetime.json` string-typed numbers (e.g. `"calls": "5"`) read as 0
   in Go; TS lets SQLite coerce them to 5. This is a one-time import only.

### Runtime and tools

- `RunCommand` options `MaxBytes`/`MaxLines`/`ForceKillAfterMs` treat 0 as "use
  the default"; TS `??` keeps an explicit 0. No TS caller passes 0.
- `BinResolution` uses `""` for a missing binary where TS uses `null`. The CLI
  prints `(missing)` for `""`, matching TS's null display.
- A job `.result.json` with a fractional `exitCode` (only reachable by
  hand-editing the sidecar) is truncated to an integer in Go; TS keeps the
  fraction.
- Name collation matches ICU for printable ASCII and falls back to code-point
  order outside it, so `find`/`read`/`ls` can differ from TS for non-ASCII
  names. `GetRtkTopPassthrough` breaks equal-count ties in byte order where TS
  uses `localeCompare`, so mixed-case tie order can differ as well.

### Web transport

- **Brotli is unresolved.** Go standard library has no Brotli decoder and no
  new dependency is allowed. Both tools send `Accept-Encoding: gzip, deflate`;
  a forced `Content-Encoding: br` body passes through undecoded (TS/Node
  decodes it). `go/internal/tools/webfetch/known-gaps.md` records the repro,
  and `TestParityBrotliKnownGap` pins the difference. No Brotli parity claim.
- **Stacked `Content-Encoding`** (e.g. `gzip, deflate`) is not decoded; the
  decoder matches a single token and the body passes through unchanged. This
  is rare and explicitly deferred; no parity claim.
- `daemon-client` models only text/image/resource content. An unknown MCP
  content-block type in a daemon reply is reported as connection-lost (never
  retried) instead of being passed through as arbitrary JSON. No cave-tools
  tool emits one.
- A standalone Go binary run outside the repository with no `CAVE_TOOLS_GO_DIST_DIR`
  cannot derive the dist-based daemon key and stays in-process.

### Server protocol

- `server` serves `tools/list` and `tools/call` through one receiving
  middleware so tool order and content blocks stay exact; results are
  semantically equal to TS and covered by the wire differential test.
- `CallTool` takes a Go map, so absent `arguments` and explicit `null` are the
  same nil map in-process. The MCP layer sees the raw JSON and rejects an
  explicit `null` with the TS SDK's zod `record` error before dispatch.
- Malformed `tools/call` `params.name` values (missing, null or non-string) are
  decoded by the Go SDK's typed request parameters, so their protocol error
  text differs from the TS SDK's zod text. Valid names, every tool schema,
  argument-record validation and all tool results match.

### Compression

- Lone-surrogate JSON escapes (`"\uD800"`) decode to U+FFFD in Go because Go
  strings cannot hold a lone code unit; TS keeps the surrogate. This is
  reachable only with deliberately malformed JSON text. Go raw UTF-8 file
  writes and JSON output are not claimed to be identical to TS for lone
  surrogates.
- `compressXML` returns `nil` for every error. TS has exactly one error today
  (`errUnparseableXML`), so callers observe the same results; a future TS error
  path needs a matching Go branch.
- `buildStats` returns a copy of `savingsByTool` where TS aliases the live
  `byTool` object. Nothing mutates a snapshot after it is returned, so emitted
  rows are identical.

## TS bugs found while porting

The port found these in the TS implementation. They are fixed in TS on `main`
(`ebe616e` "fix: TS bugs found by the Go port", `1b3fbbc` for the daemon,
`7c81d32` for the strict edit proofs, `9ee4fb1` for the webfetch HTML fix and
`e840aad` for the websearch stream cap). The Go side mirrors the fixed
behaviour on this branch. Main `6cefd81` is newer and its Go sync is still
pending.

| # | Bug | Fixed in TS | Mirrored in Go |
| --- | --- | --- | --- |
| 1 | `src/storage/db.ts` `migrateSessions`: a legacy session file with a missing `budgets`/`rtk`/`bounces` section or a `null` leaf made `upsertSessionStats` throw, so the whole session was silently dropped. | `normalizeLegacyStats` fills `{}`/0 and skips only files without a finite numeric `pid`. | `normalizeLegacyStats` in `storage/db.go`; `legacySessionAcceptedByTS` deleted. |
| 2 | `src/storage/db.ts` `updateJobState` wrote all four completion columns every time, so `updateJobState(id, { state: "lost" })` erased a recorded `exit_code`/`signal`/`ended_at`. | only the provided fields are written. | `storage.JobUpdate` uses `Optional[T]` (omitted vs explicit NULL) and `UpdateJobState` builds the SET list. |
| 3 | `src/runtime/jobs.ts` `getJobInfo` lost branch returned a synthesized object instead of the stored row. | returns `getJob(jobId)`. | `GetJobInfo` re-reads the row and falls back to the old object. |
| 4 | `src/runtime/job-worker.ts` recorded a spawn failure as exit `-1`, which clamps to 0 and looked like success. | `SPAWN_FAILED = 127` in the log, sidecar and process exit. | `spawnFailed` in `job_worker.go`. |
| 5 | `src/compression/archive.ts` printed a negative "(N lines archived)" for a few huge lines. | `lines.length <= 30` cuts by chars: first 1000 + last 500, "(N chars archived)". | same in `ArchiveIfLarge`; `utf16Suffix` keeps `slice(-500)` semantics. |
| 6 | `src/runtime/path.ts` `realPathSafe` resolved relative paths against the server's cwd, so daemon clients got server answers. | resolves against `getCwd()` first. | `RealPathSafe` resolves `resolvePath(GetCwd(ctx), value)` before `realpath`. |
| 7 | `src/runtime/bins.ts` `binDir` read `process.env`, ignoring the call env. | uses `getEnv()` with `HOME`/`USERPROFILE`/`LOCALAPPDATA` and falls back to `homedir()`. | `BinDir(ctx)` / `CachedBinPath(ctx, tool)`. |
| 8 | `src/runtime/ripgrep.ts` `Number(event.data?.line_number)` was `NaN` when rg omitted the field, and per-chunk UTF-8 decoding could split a multibyte char. | `Number.isFinite(...) ? ... : 0`; decode once from the concatenated buffers. | `line_number` decoded as a JSON number (else 0); Go already decoded once. |
| 9 | `src/compression/signatures.ts` ran the const pattern before the arrow pattern, so `const f = (a) => …` was reported as a const and its async flag read the wrong group. | arrow pattern first; `isAsync` from group 5 for arrows. | same order and group selection in `signatures.go`. |
| 10 | `src/compression/classify.ts` `/^aws\s+.*\b--output\s+json\b/i` never matched: `\b` between a space and `-` is not a word boundary. | `/^aws\s.*\s--output(?:\s+\|=)json\b/i`. | same source through `jsre`. |
| 11 | `src/compression/redact.ts` dropped the private-key END marker and the database URL's `@`, and missed `postgresql`, `mongodb+srv`, `rediss` URLs and empty users. | 3-group patterns keep the delimiters; scheme variants added. | same patterns and `${1}…${3}` replacements. |
| 12 | `src/daemon/control.ts` `rpc` parsed every reply line with an unguarded `JSON.parse`, so one malformed line crashed `daemon status`/`stop`. | `try/catch` + `continue` (`1b3fbbc`). | `daemon/control.go` already skipped malformed lines; the stale comment was updated. |
| 13 | `src/tools/edit.ts` strict proof gates stringified `null`/numbers/booleans, so `expected_hash: null` passed strict mode and the edit was written without any proof verification (found in W3b review). | `hasProofValue` requires a nonempty string in the single and batch range/move gates; the batch keeps the legacy top-level hash for its multi-file error but passes the string-only proof to `applyEditsToContent` (`7c81d32`). | `hasProofValue` in `edit.go`; `scripts/test-port-bugs.mjs` cases 18-21 are ported as `TestEditStrictProofValues`. |
| 14 | `src/tools/webfetch.ts` handled list/blockquote tags after the generic block check, so `<li>`, `<ul>` and `<blockquote>` markers were dropped; and `HEADING_TAGS[tag]` read inherited object keys, so `<constructor>`/`<toString>` were treated as headings (`9ee4fb1`). | `li`/`blockquote` handled before the generic block branch; `Object.hasOwn(HEADING_TAGS, tag)` for headings. | `webfetch.go` handles `blockquote`/`ul`/`ol`/`li` before the generic block branch and uses a Go map lookup (no prototype chain). `TestPortBugs16ListMarkers` / `TestPortBugs17InheritedObjectKeys` port cases 16-17. |
| 15 | `src/tools/websearch.ts` `collectBoundedResponse` buffered `await response.arrayBuffer()` and only checked the cap afterwards, so a chunked/streamed response was read in full (`e840aad`). | stream the body with `getReader()`, count bytes, cancel and throw at `MAX_RESPONSE_BYTES`; null body falls back to `arrayBuffer()`. | `collectBoundedResponse` reads through `io.LimitReader(body, MaxResponseBytes+1)` and rejects above the cap without buffering the rest; streaming/declared-length/null-body/split-UTF-8 cases 22-25 are ported in `websearch_differential_test.go`. |

Independently of the table, the W3b review found Go's edit handlers rejecting
an explicit `null` line number where TS's `Number(null) === 0`: Go now
distinguishes an absent key (`Number(undefined)` is NaN) from a present null in
single and batch range/move and the batch `delete:true` path, preserving the
shape gates. The batch planners also stringify a present
`expected_range_checksum` like TS (`String(null)` is `"null"`), so an explicit
null fails verification instead of being skipped.

Differential testing also found Go-only mismatches that were not TS bugs:
`fa5d968` mirrors JavaScript `String()` for off-schema
`expected_hash`/`expected_range_checksum` values (objects, arrays with null
slots, large floats, `-0`, and the 1e-6/1e-7/1e20/1e21 boundaries). It adds 18
cases; before the fix, enforce differed on 15 of 18 new cases and strict on 6
hash cases. The TS side was already correct.

## Coverage and annotation audit

- The map above covers all 56 `src/**/*.ts` files and every target exists.
- 232 exported TS function/const-arrow names were all found in Go source
  (exact or exported form) in a static audit; this is a name-level check, not
  proof that each function body was compared or tested.
- 806 `// port of` provenance lines cover all 56 TS files on this branch.
  Counts per file are not evidence of completeness: some TS types, constants
  and module-level state are annotated under their Go names, and a few helpers
  are referenced only in prose.
- Known annotation gap (report only, no Go edit in W5c): `storage.scanJob` is
  annotated with prose "This is the Go form of
  `src/storage/db.ts:jobFromRow`" rather than the canonical
  `// port of src/storage/db.ts:jobFromRow`. The implementation exists and is
  exercised; the annotation should be normalised in a later Go edit.
- `src/plugins/**` is adapter code listed above and is not expected to have a
  Go counterpart.

## Verification status

Evidence recorded for this document. Do not turn any line into a broader claim
than it states.

- Run in this worktree (`migration/w5-docs` at `02890a1`):
  - `go test ./internal/tools/edit -run TestDifferentialAgainstTypeScript -count=1`
    → enforce and strict: 94 cases, 3 schemas, 0 result mismatches, 90 files
    identical in each mode.
  - `go test ./internal/tools/webfetch ./internal/tools/websearch -run 'TestPortBugs16ListMarkers|TestPortBugs17InheritedObjectKeys|TestStreamingResponseStopsAtCapAndCloses|TestStreamingDeclaredOversizeDoesNotRead|TestCollectBoundedResponseStopsAtCap' -count=1`
    → 5 tests passed in 2 packages (targeted HTML/cap mirrors).
  - Map/path audit: 56/56 rows, all TS and Go target files present.
- Coordinator evidence for W4 (not rerun in this docs task): final combined
  offline gate at `ba8d079` — gofmt, vet, full tests, parity with 430
  web+CLI/daemon roundtrip comparisons, race packages, Windows/darwin vet;
  job `jobjmvm1wsiz04fecf`. Treat the job id as a pointer, not a substitute for
  rerunning at release.
- TS reference: `main` at `e840aad` passed `pnpm build && pnpm test` with 12
  suites and 25 port-bug tests (coordinator run). Main is now `6cefd81`; its
  additional shell-guidance changes and suite totals are not restated here
  until the W5 sync runs them against the Go branch.
- Not verified / pending:
  - `CAVE_TOOLS_IMPL=go` integration, missing-binary behaviour, and the
    switch parity case (W5 switch worktree; no claim here).
  - Go sync to `main` `6cefd81` (W5 sync worktree).
  - Brotli and stacked `Content-Encoding` parity (unresolved/deferred).
  - Windows runtime behavior: only `GOOS=windows go vet` is claimed. The
    Unix-socket daemon interop tests skip on Windows, so named-pipe interop is
    not proven by tests.
  - Full function-by-function equivalence for every TS function; only the
    mapped files, differential/parity cases and listed tests are evidence.

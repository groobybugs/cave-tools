# cave-tools Go port — porting contract

The TypeScript implementation in `src/` is the reference and keeps working.
The Go port lives in `go/` and is ported function by function, file by file.
Both implementations are designed to be interchangeable: same output bytes,
same error texts, same exit codes, same files on disk, same SQLite schema and
data dir. Interchangeability is a per-behaviour claim backed by tests, not a
blanket statement about every function; see "Verification status" and "Known
deviations and limitations".

## Status

- W0–W4 are integrated on `go-port` (proof-coercion fix `fa5d968` included).
  The W5 main sync is merged at `d163dae`; all 56 engine files listed below
  have mapped Go implementations. The switch adds one TS-only adapter,
  `src/impl-switch.ts`, bringing the current TS inventory to 57 files.
- Source truth for the TypeScript reference is `main` at `6cefd81`.
  `d163dae` merges that commit: `src/`, `claude/`, `scripts/`, `rules/`,
  `AGENTS.md` and `CLAUDE.md` have zero diff against `6cefd81`. The sync adds
  two Go mirrors (the CLI instruction text and the bash tool metadata) and
  exact init/metadata tests; see "Verification status".
- W5 is at its final integration state: main sync (`d163dae`), the opt-in
  hybrid switch (`a0dcc6e`) and the expanded parity suite (CLI black-box, MCP
  wire, daemon and durable-job interop) are integrated. The W5 fix set is
  merged: byte-wise read suggestion order, the empty `bash` command rejection,
  JS `String.prototype.toLowerCase` through `jsre.ToLowerJS` with the Node
  24.19.0-measured Unicode delta tables, and the V8 `JSON.parse` error text,
  Node-compatible stdin UTF-8 decoding and ±Infinity numbers. The last three
  land on `go-w5-fix-b`; they are written here as part of W5 because the
  coordinator merges that branch before `go-port` advances. Measurements are in
  `scripts/bench-ts-go.md` and summarized under "Performance".
- TypeScript remains the default implementation. `CAVE_TOOLS_IMPL=go` opts
  into the locally built Go binary; see "Implementations switch". No live
  user installation or configuration was changed.

### Platform scope

- **Linux is the supported target.** Every runtime gate (daemon Unix sockets,
  signals, durable job workers, parity) runs on Linux.
- **macOS is planned.** `GOOS=darwin go vet ./...` passes, but no macOS runtime
  run is claimed.
- **Windows runtime is TODO.** Only `GOOS=windows go vet ./...` is claimed.
  Named pipes, signal forwarding and MSYS path translation are untested, and
  the Unix-socket daemon interop tests skip on Windows.

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

Each of the 56 engine TS files maps to a Go file with the same base name
(kebab-case → snake_case). Packages mirror the TS directories; top-level TS
files live in the package that owns their responsibility (`server` for the MCP
entry layer, `cli` for the CLI, `daemon` for the socket client). The additional
`src/impl-switch.ts` is TS-only launcher glue and has no Go counterpart: the
Go executable does not need to select itself.

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

All 56 rows were checked against the tree at `02890a1` and again after the
`d163dae` sync: every mapped TS file and Go target exists. The later switch
adds `src/impl-switch.ts` outside this engine map, as explained above. The sync changed guidance text
in `src/cli.ts`/`src/tools/bash.ts`, not the file mapping. The map itself is
not proof of function coverage; see "Coverage and annotation audit".

`go/internal/cli` additionally holds helpers that belong to the two CLI files
above rather than separate TS modules: `status_summary.go` (thin wrappers over
`go/internal/statusline`), `cli_raw.go` and `cli_platform_*.go` (raw shell
execution and Win32 `cmd.exe` quoting), `integration.go` and
`bindings_{server,daemon}.go` (the server/daemon seams `cli-tools.ts` reaches
through its lazy imports).

`go/internal/statusline` mirrors `src/statusline.ts`: it aggregates lifetime +
visible-session stats from SQLite, formats the compact badge and writes
`~/.claude/.cave-tools-statusline-suffix` atomically. Both
`status --emit-statusline` and the MCP server's debounced post-call refresh
call into it, so the badge bytes cannot drift between the CLI and MCP paths.

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
| `go/internal/runtime/cmdline.go`, `go/internal/runtime/locale.go` | Node `cmd.exe` command-line construction; `localeCompare` collation for names (`x/text/collate` with ICU's default-locale env resolution). |
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
into W3. W5 is integrated; see "Status".

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
- **W5 integration**: main sync completed at `d163dae` (shared files zero diff
  vs `6cefd81`; two Go mirrors plus exact init/metadata tests),
  `CAVE_TOOLS_IMPL=go` switch integrated at `a0dcc6e`, benchmark harness
  `106940f`, and the expanded parity suite and fix set described under
  "Status".

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
| W5 | main sync (done at `d163dae`), remaining parity expansion, launcher switch, benchmark, docs/audit |

`go.mod` is W0-only: later waves must not add dependencies. If one is truly
needed, ask the coordinator.

## Implementations switch (W5, integrated)

Default stays TypeScript. Switch commits `8fea696` and `793d072` are integrated
at `a0dcc6e`; no user installer was run.

```sh
pnpm build
(cd go && GOPROXY=off GOSUMDB=off go build -o bin/cave-tools-go ./cmd/cave-tools-go)
CAVE_TOOLS_IMPL=go node dist/cli.js help
```

- Only exact lowercase `CAVE_TOOLS_IMPL=go` selects Go. Unset, empty and other
  values retain TypeScript.
- `CAVE_TOOLS_GO_BIN`, when nonempty, overrides the binary path. Relative
  paths resolve against caller cwd. Otherwise the binary is
  `<package>/go/bin/cave-tools-go` (`.exe` on Windows). Point the override at
  an actual Go executable, not a TS launcher or wrapper that re-enters the
  switch.
- `src/impl-switch.ts` forwards argv, env, cwd and inherited stdin/stdout/stderr,
  mirrors exit status and relays supported signals. Every non-TS-routed mode,
  including `__job-worker`, passes through this one boundary.

### Routing table (hybrid switch)

Go serves the short-lived CLI paths. The long-lived stdio MCP server and the
two Node-fetch-backed web tools stay in TypeScript even when
`CAVE_TOOLS_IMPL=go`:

| argv form | Runs | Why |
| --- | --- | --- |
| *(no arguments)* | TypeScript | a bare invocation starts the stdio MCP server |
| `mcp` (any trailing args) | TypeScript | long-lived stdio session: no startup win, and the Go compression step is slower |
| `webfetch <url> …` | TypeScript | depends on Node `fetch`/undici behaviour |
| `websearch <query> …` | TypeScript | depends on Node `fetch`/undici behaviour |
| `tool webfetch <url> …` | TypeScript | the explicit dispatcher loads the same Node-fetch handler as the direct form |
| `tool websearch <query> …` | TypeScript | same as `tool webfetch` |
| everything else (`__job-worker`, `filter`, `status`, `raw`, `jobs`, `daemon`, `help`, `tool <other>`, `read`/`bash`/… tool commands, unknown commands) | Go | short-lived; the pre-Node Go exec is the win |

- The decision is made from the **argv tokens exactly as `src/cli.ts`
  dispatches** (argc 0 is the MCP case). The first token decides, except for
  the `tool` dispatcher, where the second token decides whether the target is
  a TS-routed tool. Trailing flags and subcommands never change the decision:
  `mcp --stdio` is still the MCP server, `webfetch --help` and
  `tool webfetch --help` stay in TypeScript, and `tool webfetch <url>` cannot
  fall through to Go. `cave-tools help webfetch` and `cave-tools tool <other>`
  still go to Go like every other `help`/`tool` invocation; their output is
  byte-identical to TypeScript.
- Single source of truth: the canonical lists live in
  `scripts/impl-routes.mjs` (`TS_ROUTED_COMMANDS` for the direct commands,
  `TS_ROUTED_TOOLS` for the tools reachable through `tool <name>`).
  `src/impl-switch.ts` imports them for the runtime check and
  `scripts/install-agent-instructions.mjs` interpolates the same lists into the
  generated POSIX shim's nested `case`, so the two cannot drift.

- Rationale: the MCP server is long-lived, so skipping Node startup buys
  nothing and the Go compression path is slower; `webfetch`/`websearch` depend
  on Node `fetch`/undici semantics, which are the reference behaviour.
- The Go stdio MCP server and the Go `webfetch`/`websearch` implementations
  remain in the tree; they are reachable only by invoking the Go binary
  directly (`go/bin/cave-tools-go mcp`, `… webfetch <url>`, `… websearch
  <query>`). Their documented gaps (Brotli, stacked Content-Encoding, zstd) are
  exactly why the switch does not send these surfaces there. No Go code was
  removed.

- Direct `node dist/cli.js` selects Go before CLI command execution (unless
  argv is TS-routed), but static imports still initialize first. It does not
  avoid Node startup. Newly generated POSIX installer shims run the same
  argc/first-token (plus `tool` second-token) check and then select Go with
  `exec` before starting Node; static paths and caller arguments are quoted,
  including spaces and shell metacharacters.
- A missing binary exits 127; direct Node reports a non-executable path (or a
  directory) with 126. The POSIX shim deliberately collapses both cases to
  127, so the same misconfiguration has a different code depending on the
  entry point. There is no silent TS retry of a selected Go operation.
- Windows still uses the existing package-manager launcher through Node; no
  pre-Node `.cmd` shim was added. Windows `.exe` resolution is unit-tested,
  not Windows runtime acceptance. Windows signals and named-pipe runtime
  interop remain unverified.
- No install, dependency or PATH change is required to build locally. Both
  implementations share the data dir, SQLite schema, daemon key, socket and
  NDJSON protocol. Switching does not require stopping a compatible daemon
  built from the same `dist/`; normal reconnect/spawn behavior applies after
  rebuilding.
- `scripts/test-impl-switch.mjs` covers default/opt-in selection, hybrid
  routing (every TS-routed form and representative Go-routed forms, through
  both the direct Node entry point and the generated shim), fake and real Go
  binaries, missing/non-executable paths, argv/stdin/exit/signal forwarding,
  bare-relative overrides and generated POSIX shims in private fake homes.
  The coordinator ran this suite with a real local Go binary and no skips.
  TS reference test children clear switch variables to avoid accidentally
  comparing Go against itself.

## Performance and measurement

Machine-specific numbers from a 14-day trace on one Linux workstation. They are
not portable benchmarks; the reproducible harness and provenance pins are in
`scripts/bench-ts-go.md`.

- ~147 CLI starts/day, median 40 starts per session, p90 190.
- 85% of starts are the async `status --emit-statusline` hook that runs after
  each cave MCP tool call, so they are off the critical path.
- 13% are `filter --json`, the only blocking path: ~112 ms → ~50 ms with Go.
- Per-start saving is ~55 ms when the launcher `exec`s the Go binary directly,
  ~30 ms when startup goes through the `cave-node` shim.
- Node-only hooks (~43 ms each) are unaffected by Go; they still pay Node
  startup.

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
- The statusline badge is a shared TS/Go side effect: the MCP server refreshes
  `~/.claude/.cave-tools-statusline-suffix` after each `tools/call` with a
  ~300 ms trailing-edge debounce and a shutdown flush, instead of spawning
  `cave-tools status --emit-statusline` per call. `status --emit-statusline`
  uses the same aggregate/format/write path, and
  `internal/cli/status_summary_test.go` compares both paths byte-for-byte in a
  fresh subprocess. Tests must keep `CLAUDE_CONFIG_DIR` inside their sandbox
  (the `internal/server` TestMain does).
- The badge write is temp-file + rename, so an existing badge is recreated with
  mode `0600` instead of keeping its previous mode. That stricter mode is
  intended (both writers now agree, and the tests assert it); the only
  side effect is that a crash between temp create and rename can leave a
  `.cave-tools-statusline-suffix.*.tmp` file behind. The MCP server has no
  SIGTERM/SIGINT handler, so a signal-killed server drops at most one pending
  debounce window; stdin EOF and normal shutdown flush. TS behaves the same
  way (`process.once("exit")` does not run on those signals).

## Known deviations and limitations

These are verified against the code and tests at the W5 integration tip (base
`36117d9`, main sync `d163dae` plus the W5 fixes). They are **not** blanket
parity claims and none of them is labelled "accepted parity"; each needs a
follow-up decision or stays a documented limit.

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
- `rtk rewrite` (`compression/utils.go` `runRtkRewrite`): TS calls `execFile`
  with Node's default `maxBuffer`, so an oversized stdout can fail there and
  leave only partial output; Go reads the full buffer. The 3s timeout normally
  dominates, and no fix/test is claimed. This is separate from `rtk gain`,
  which does implement the 2 MiB `limitedBuffer` cap. (W2k review, low.)
- A job `.result.json` with a fractional `exitCode` (only reachable by
  hand-editing the sidecar) is truncated to an integer in Go; TS keeps the
  fraction.
- **Name collation residual differences (CLDR version skew).** `find`,
  `read` (directory listing), `ls` and `GetRtkTopPassthrough` now collate with
  `golang.org/x/text/collate` under the locale Node would pick (see below), so
  names sort in ICU order instead of byte/printable-ASCII order. The remaining
  differences come from the locale data version, not the wiring: `x/text`
  v0.43.0 ships CLDR 23 / Unicode 6.2 collation tables, while Node 24.19 runs
  ICU 78.3 / CLDR 48. The differential corpus
  (`go/internal/runtime/testdata/collate_corpus.json`, 27,266 names x 5 locales,
  generated by `gen_locale_fixture.mjs`) measures it:
  - Every name outside the two skew classes below appears in Node's exact
    *relative* order for en-US (`LANG=en_US.UTF-8`), `C`, es-ES, sv-SE and
    tr-TR (22,785 of 27,266 names; 12,267 of them pure printable ASCII, all
    outside the skew classes).
  - Absolute positions are not preserved: the 4,481 skew names (16.4%) sit
    between the others, so indices shift. Only 596 of the 27,266 names land on
    Node's exact index (572 of the 22,785 non-skew names, ~2.5%), and the worst
    displacement is 24,762 places (`"🧑🏿‍🚀x"`, which ICU puts near the top of
    the emoji block and the CLDR 23 table near the bottom). The counts are the
    same for all five locales.
  - Adjacent-pair concordance with Node's order is 0.99956 for every locale
    (12 reversed pairs of 27,265, i.e. the 6 displaced names listed below);
    sampled pair signs are 1.00000/0.99944/0.99944/0.99889/0.99944 for
    C/en-US/es-ES/sv-SE/tr-TR.
  - The 12 reversals are 6 displaced names, both classes known to have changed
    between CLDR 23 and 48:
    - Emoji from Unicode 12+: `"🧑🏿‍🚀"` (U+1F9D1 U+1F3FF ZWJ U+1F680, the
      skin-tone modifier is Emoji 5.0) sorts next to the other emoji in ICU 78
      but as an unassigned code point in the CLDR 23 table. Concrete pair:
      `".🧑🏿‍🚀"` before `".😀"` in Node, after it in Go; same for
      `"🧑🏿‍🚀x"`/`"😀"` and `"x🧑🏿‍🚀"`/`"x😀"`.
    - Han collation data (Han readings/radical-stroke were regenerated after
      CLDR 23). Concrete pairs, all in en-US/es-ES/sv-SE/tr-TR:
      `"中试试語さ"` before `"中語京汉"`, `"京试都川う"` before `"京語"`,
      `"字试山か"` before `"字語"`, `"川试都語エ"` before `"川語田阪"`,
      `"文试本田测に"` before `"文語川本"`, `"汉试中中川ア"` before
      `"汉語山京"`, `"试阪文田阪そ"` before `"語"`, `"語试阪ね"` before
      `"語語大字"`, `"都试字字大そ"` before `"都語山中山"` (Node puts the
      `語`-initial names first; Go puts `试` first). Pure-Latin, Greek,
      Cyrillic, Arabic, Hebrew, Thai, Devanagari and Hangul names match
      exactly.
  - Pair-sign residuals are the same emoji/Han pairs plus `"🧑🏿‍🚀x"` vs
    `"צנלר"` (Hebrew), where ICU sorts the new emoji before Hebrew and the
    CLDR 23 table after it.
  - A realistic 227-name corpus (`go/internal/runtime/testdata/
    realistic_corpus.json`: accented Latin, ß/umlauts, ñ, İ/ı, å/ä/ö, Greek,
    Cyrillic, a few CJK/emoji names, NFC/NFD twins, `file1`/`file2`/`file10`,
    dotfiles, mixed case and separators) sorts in Node's exact absolute
    positions for all five locales. Both the unit assertion
    (`TestLocaleCollationRealisticCorpus`) and the real CLIs agree: 15/15
    unpaginated and 75/75 paginated `ls`/`read`/fallback-`find` runs are
    byte-identical, and `read` shows all 227 names in the same absolute order
    in every locale. The skew classes above cost nothing on ordinary names.
- **Locale selection** mirrors ICU's POSIX path, measured from Node 24.19:
  `LC_ALL` > `LC_MESSAGES` > `LANG` (`LC_COLLATE` is irrelevant for
  collation). A variable that is *set but empty* wins over the ones after it
  and yields the empty root locale, which V8 reports as `und`; when none of the
  three is set, or the winner is `C`/`POSIX` (also `C.UTF-8`, after the codeset
  is stripped), ICU falls back to `en_US_POSIX`, i.e. `en-US`.
  `go/internal/runtime/testdata/locale_env.json` records 54 such
  environments and `TestResolveLocaleMatchesNode` replays them, including the
  probe order. On Windows, ICU derives the default locale from the OS user
  locale instead of these variables; the Go port still reads the environment,
  so a Windows machine whose user locale is not English can collate
  differently from Node. macOS shows the same shape of residual for a different
  reason: ICU there takes the default from the system locale
  (NSLocale/AppleLanguages) and ignores `LC_ALL`/`LC_MESSAGES`/`LANG`, while Go
  reads the environment on darwin too, so a Mac whose system locale differs
  from the environment-derived tag can collate differently from Node. Both
  branches are compile-verified only (`GOOS=windows go vet`,
  `GOOS=darwin go vet`).
- The `x/text` collator is not safe for concurrent use, so comparisons borrow
  a collator from a per-locale `sync.Pool`; sorts precompute one key per name
  into a shared arena and order the keys with a stable sort, which keeps V8's
  sort stability for names the collator considers equal (canonically
  equivalent NFC/NFD pairs, soft hyphens, zero-width characters). No second
  comparator or extra tie-break is mixed in: the printable-ASCII fast path is
  limited to `en`/`und` locales and proven sign-identical to the collator over
  all 95x95 ASCII character pairs, all two-character strings from a
  case/punctuation/digit alphabet and 20,000 random ASCII strings
  (`TestASCIICollationMatchesCollator`); locales that tailor ASCII (tr, az,
  aa, et, haw, lt, ssy, en-US-u-va-posix) always use the collator.
- `read`'s directory listing now byte-sorts the `readdir` result before the
  locale sort, because libuv's `scandir` sorts with `strcmp` in TS. Without it,
  names the collator ties on (NFC vs NFD, ignorables) came out in filesystem
  order instead of Node's.
- Cost of the switch (`go test -bench` on `internal/runtime`, Intel Ultra 9
  275HX): a 10,000-name sort takes 6.4-6.8 ms with the stable key sort, versus
  9.6-10.2 ms for the previous rune-based comparator (bytewise sort, which is
  not a candidate: 1.4 ms). One `LocaleCompare` call costs 37-54 ns for ASCII
  and 1.7-1.8 us for a 24-character Cyrillic name (the old comparator: 45-54 ns
  and 186 ns), so comparator-based callers should stay small; every sort site
  in the port uses the key path.

### Web transport

- Under the opt-in switch, `webfetch` and `websearch` stay in TypeScript, so
  they run the reference Node/undici stack and the gaps below are **not** on
  the routed path. The Go-native web code is reachable only by invoking the Go
  binary directly (`go/bin/cave-tools-go webfetch <url>`,
  `… websearch <query>`). That code keeps:
  - **Brotli.** Go's standard library has no Brotli decoder and no new
    dependency is allowed. The tools advertise `Accept-Encoding: gzip,
    deflate`; a forced `Content-Encoding: br` body passes through undecoded
    (Node decodes it). `go/internal/tools/webfetch/known-gaps.md` records the
    repro and `TestParityBrotliKnownGap` pins it. No Brotli parity claim.
  - **Stacked `Content-Encoding`** (e.g. `gzip, deflate`) is not decoded; the
    decoder matches a single token and the body passes through unchanged.
  - **zstd** is not decoded. Node fetch handles it; Go advertises only `gzip`
    and `deflate`, and a forced zstd body passes through.
  - Gzip trailing-junk tolerance and the pre-decode `Content-Length`
    mismatch check are pre-existing Go-native behaviour; they are not claims
    about the routed path.
  Branch `go-w5-fix-c` (andybalholm/brotli plus a shared decoder) was **not
  merged**, by decision: W5 adds no new Go dependency.
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
- The JSON-RPC envelope's key order can differ from the TS SDK's insertion
  order. Clients parse JSON, and every payload value matches; the wire
  differential test compares decoded frames, not raw key order.

### Compression

- Lone-surrogate JSON escapes (`"\uD800"`) decode to U+FFFD in Go because Go
  strings cannot hold a lone code unit; TS keeps the surrogate. CLI text
  output converges: both sides print U+FFFD. JSON does not: the TS MCP server
  keeps the `\uXXXX` escape for a lone surrogate while Go emits U+FFFD.
- JS-behaviour fixtures are pinned to node 24.19.0 (ICU 78.3, Unicode 17.0):
  `internal/jsre/testdata/lower_node.txt` and `lower_sequences.txt` for
  `ToLowerJS`, plus the measured JSON/whitespace expectations in
  `internal/jsre/jsre_test.go`. The regeneration one-liners are listed in the
  `internal/jsre/lower_test.go` header. A different Node version changes the
  fixtures, not the implementation; the delta tables in
  `lower_unicode17.go` shrink if Go's Unicode tables catch up.
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
behaviour on this branch. Main `6cefd81` is synced as `d163dae`; the only Go
changes it required were the CLI instruction text and bash tool metadata, not
new handler behaviour.

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

- The map covers all 56 engine TS files and every target exists. The 57th
  TS file, `src/impl-switch.ts`, is adapter-only and intentionally unmapped.
- An ad-hoc static audit checked exported TS function/const-arrow names against
  Go source; no missing exported callable was found. It is name-level only, its
  extraction regex is not committed, and it is not proof that each function
  body was compared or tested. No count is asserted.
- Provenance annotations are non-test `// port of src/<file>.ts:<symbol>`
  comments. To reproduce the line count (not a coverage measure):

  ```sh
  grep -rn "// port of src/" go --include='*.go' --exclude='*_test.go' | wc -l
  ```

  The scope matters: `// port of` including test files is larger and must not
  be compared with the non-test count. No numeric count is asserted here;
  annotations change as provenance is clarified.
  Counts per file are not evidence of completeness either: some TS types,
  constants and module-level state are annotated under their Go names, and a
  few helpers are referenced only in prose.
- The `storage.scanJob` provenance gap is closed by coordinator commit
  `3d737eb`: its prose reference is now the canonical
  `// port of src/storage/db.ts:jobFromRow` annotation. No executable code
  changed; the whole Go suite passed `go test -race -count=1 ./...` at that
  commit (job `jmvmto0810d0cae`).
- `src/plugins/**` is adapter code listed above and is not expected to have a
  Go counterpart.

## Verification status

Evidence recorded for this document. Do not turn any line into a broader claim
than it states.

- W5 docs branch `groobybugs/go-w5-docs` (base `36117d9`), Linux, with the
  parent shell's `CLAUDE_CODE_*` variables present:
  - `go vet ./...`, `GOOS=windows go vet ./...`, `GOOS=darwin go vet ./...` →
    clean.
  - `go test -count=1 ./...` → 1682 pass, 2 skip, 0 fail (28 packages); the
    same counts with those variables unset.
  - `go test -tags parity -count=1 ./parity/` → 222 pass, 1 failure:
    `TestCLIBlackBoxDifferential/filter.invalid-json`, the `go-w5-fix-b` gap
    (V8 `JSON.parse` error text). With the variables unset: 221 pass and the
    same failure, plus one `errors.live-single` sampling flake (below).
  - `go test -race -count=1` on the changed packages (`cli`, `daemon`,
    `runtime`, `server`, `storage`, `testenv`, `tools/...`) → clean. `-race`
    parity → no data races, same `filter.invalid-json` failure.
  - Test hermeticity: every Go test that spawns the TS CLI/MCP/daemon scrubs
    the subagent-like environment keys through `go/internal/testenv`, derived
    from the detection predicates in `src/compression/utils.ts`
    (`CLAUDE_CODE_*`, any `*SUBAGENT*`, `*_TASK_ID`, `CAVE_TOOLS_FORCE_FRESH`).
    `scripts/test-impl-switch.mjs` applies the same rule to the children it
    spawns through the Go shim.
  - Pre-existing flake, not caused by that scrub: `errors.live-single` can
    sample 0 persisted Go tool errors when the process exits right after a
    failed read (base `36117d9`: 8/100 runs, this branch: 5/100). A second
    pre-existing flake, `TestMCPPipeInitializeListCallError` failing its
    `TempDir` cleanup with `directory not empty` when a background write lands
    too late (base: 2/100, this branch: 5/100). Neither is fixed here; both are
    recorded as residuals.

- Run on this docs branch before the sync merge (tip `6d64a7a`, code baseline
  `02890a1`):
  - `go test ./internal/tools/edit -run TestDifferentialAgainstTypeScript -count=1`
    → enforce and strict: 94 cases, 3 schemas, 0 result mismatches, 90 files
    identical in each mode.
  - `go test ./internal/tools/webfetch ./internal/tools/websearch -run 'TestPortBugs16ListMarkers|TestPortBugs17InheritedObjectKeys|TestStreamingResponseStopsAtCapAndCloses|TestStreamingDeclaredOversizeDoesNotRead|TestCollectBoundedResponseStopsAtCap' -count=1`
    → 5 tests passed in 2 packages (targeted HTML/cap mirrors).
  - Map/path audit: 56/56 rows, all TS and Go target files present; rechecked
    after merging `d163dae`.
- W5 main sync at `d163dae` (coordinator evidence, not rerun in this docs
  task):
  - `src/`, `claude/`, `scripts/`, `rules/`, `AGENTS.md` and `CLAUDE.md` have
    zero diff against `main` `6cefd81`.
  - Two Go mirrors: the CLI instruction constant in `internal/cli/cli.go` and
    the `cave__bash` description in `internal/tools/bash/bash.go` (schema
    unchanged).
    `TestInitInstructionsMatchTypeScript` runs `node dist/cli.js init` and
    byte-compares `CLAUDE.md`/`AGENTS.md`; bash `TestToolMetadata` pins the
    exact description and full input schema, and the CLI help golden pins the
    user-visible description.
  - `pnpm build && pnpm test` → 13 suites, plus gofmt/diffcheck PASS;
    job `jmvmt2rg9e72051`.
  - After that build: full offline Go vet/test, parity scope `./parity` plus
    `webfetch`, `websearch` and `compress`, race on `cli`/`bash`/`edit`, and
    Windows+Darwin vet all PASS; job `jmvmt3fh0d37266`.
  - Job `jmvmt2rgb996949` was deliberately canceled during a concurrent TS
    build and is superseded by `jmvmt3fh0d37266`; it is not evidence.
- Coordinator evidence for W4 (not rerun in this docs task): final combined
  offline gate at `ba8d079` — gofmt, vet, full tests, parity with
  coordinator-reported 430 web+CLI/daemon roundtrip comparisons (count not
  re-derived in this docs task), race packages, Windows/darwin vet;
  job `jobjmvm1wsiz04fecf`. Treat the job id as a pointer, not a substitute for
  rerunning at release.
- TypeScript reference: `main` at `6cefd81`, synced as `d163dae`, passed
  `pnpm build && pnpm test` with 13 suites and gofmt/diffcheck (job
  `jmvmt2rg9e72051`). The port-bug script still declares 25 cases.
- W5 switch integration at `a0dcc6e` (coordinator-run):
  - `pnpm check && pnpm test && git diff --check` passed all 14 suites,
    including `impl-switch` with a real Go binary and no skips;
    job `jmvmturpj72c707`.
  - After that TS build: gofmt, full offline Go vet/test, `-tags parity
    ./parity/`, Windows vet and Darwin vet passed; job `jmvmtvf3pa16619`.
  - Muse reviewed the switch and its bare-relative/quoting followup clean.
- Benchmark harness `106940f` was Muse-reviewed and coordinator-run on frozen
  TS `6cefd81` / Go `5f28d88` artifacts: 30 samples per side per route, seeds
  42 and 73, all four CLI routes byte-matched and three MCP routes had identical
  ToolResult payloads (envelope key order differs, explicitly flagged). Every
  timed sample passed validation; provenance artifact pins matched. Results,
  profiling evidence and limitations are in `scripts/bench-ts-go.md`.
- Not verified / pending:
  - `go-w5-fix-b` (`filter.invalid-json`: V8 `JSON.parse` error text, Node
    stdin UTF-8, ±Infinity numbers) is not on this branch; the parity case is
    expected to fail until the coordinator merges it. It is written as part of
    W5 in "Status".
  - Brotli, stacked `Content-Encoding` and zstd parity in the Go-native web
    path: `go-w5-fix-c` was not merged, by decision.
  - Windows runtime behavior: only `GOOS=windows go vet` is claimed. The
    Unix-socket daemon interop tests skip on Windows, so named-pipe interop is
    not proven by tests. macOS has no runtime run either.
  - Full function-by-function equivalence for every TS function; only the
    mapped files, differential/parity cases and listed tests are evidence.

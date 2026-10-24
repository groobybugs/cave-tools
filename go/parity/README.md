# Go ↔ TypeScript parity harness

Runs `node dist/cli.js <args>` and the Go binary with the same argv, stdin and
env (each side gets its own temp `CAVE_TOOLS_DATA_DIR`, a private
`CLAUDE_CONFIG_DIR` and `CAVE_TOOLS_DAEMON=0`), then compares stdout, stderr
and the exit code.

## Run

```sh
pnpm build
cd go
go test -tags parity ./parity/
```

`go test ./...` skips the harness: `parity_test.go` carries the `parity` build
tag, so only the package comment is compiled.

## Adding a case

Add one row to `parityCases` in `parity_test.go`:

```go
{
    name:  "status",
    args:  []string{"status", "--verbose"},
    stdin: "",
    env:   map[string]string{"CAVE_TOOLS_STATUS_RTK": "0"},
},
```

Every row is `{name, args, stdin, env}` and is compared byte-for-byte.
`goOnlyExtension` is **not** a divergence waiver: it marks a command the Go
binary intentionally adds (currently only `--version`, which `src/cli.ts` does
not implement) and pins both the TypeScript reference output and the Go
extension output, so a change on either side fails the harness.

Cases must stay hermetic: never point `CAVE_TOOLS_DATA_DIR` or
`CLAUDE_CONFIG_DIR` at the developer's real directories, and keep stdin finite
(an MCP server case would block forever without `stdin` EOF).

## Black-box suites (same build tag)

The argv table above covers command routing. The following suites run the real
binaries against twin fixture trees and compare stdout, stderr, exit codes and
filesystem side effects:

- `cli_blackbox_test.go` — every tool command, flag coercion, stdin values,
  media, write/edit/patch/configure side effects, seeded jobs/errors, init,
  raw/filter, statusline side effects and daemon-control output.
- `mcp_blackbox_test.go` — MCP initialize, `tools/list` schemas/profile/order,
  `tools/call` results including media and `_meta` sessions, error/argument
  handling, stdout cleanliness and the error-log privacy switch.
- `daemon_interop_test.go` — Go CLI/MCP clients against the TypeScript daemon
  and TypeScript CLI/MCP clients against the Go daemon, including explicit CLI
  sessions across processes, cwd/env, stats isolation and persisted budgets.
- `state_interop_test.go` — durable jobs, archives, session stats and budget
  rows produced by one implementation and consumed by the other.

`sandbox_test.go` holds the twin-sandbox harness; `state_helpers_test.go` seeds
rows through the TypeScript storage layer and reads state back through the
shared schema.

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

Every row is `{name, args, stdin, env}` and is compared byte-for-byte. Use
`knownDivergence` **only** while the Go side is still a stub: it pins each
implementation to its own expected output (`wantTS` / `wantGo`) instead of
comparing them, and the reason is printed with `-v`. Delete it once both sides
agree.

Cases must stay hermetic: never point `CAVE_TOOLS_DATA_DIR` or
`CLAUDE_CONFIG_DIR` at the developer's real directories, and keep stdin finite
(an MCP server case would block forever without `stdin` EOF).

# bench-ts-go — TypeScript vs Go benchmark methodology

Standalone harness: `node scripts/bench-ts-go.mjs`. Node built-ins only, no
dependencies, no new Go modules, nothing installed. The Go binary is built
locally and offline (`GOPROXY=off GOSUMDB=off`) as `cave-tools-go` (never
`cave-tools`, never installed); the TS side runs an absolute `dist/cli.js`
entry directly and is never rebuilt or modified by the harness. Both entry
paths are explicit flags and every run records the exact commits it measured.

## Routes and what "fast" means for each

Core routes (default):

| route | workload | timing scope |
|---|---|---|
| `cli_help_cold` | `help` (startup + tool help load) | fresh process per sample — **TS: Node startup remains in every sample** |
| `cli_filter_medium` | `filter --json` on ~32 KiB deterministic output | fresh process per sample (startup + full compression pipeline) |
| `cli_filter_large` | `filter --json` on ~450 KiB deterministic output | same; exercises archive-handle path |
| `cli_read_cold` | `read <fixture>` | fresh process per sample = dedup miss / first-full read (file cache is in-memory per process) |
| `mcp_read_first` | `tools/call cave__read`, fresh session id | warm persistent MCP server; timed value is the tools/call roundtrip only |
| `mcp_read_dedup` | `tools/call cave__read`, same session + unchanged file | same session; dedup stub path |
| `mcp_compress` | `tools/call cave__compress` (~25 KiB text) | same session |

Optional (off by default, deferred):

| route | flag | status |
|---|---|---|
| `daemon_cli_read` | `--daemon` | deferred — see limitations |
| memory phase (`/usr/bin/time` RSS, VmHWM) | `--memory` | optional, runs strictly after timing |

All "cold" labels are **process-cold only**. The harness never drops the OS
page cache and makes **no cold-filesystem claims**. "Warm" means a persistent
process (MCP server) that has been initialized and received untimed warmup
calls before timing.

## Environment, fixtures, isolation

Both sides run with a byte-identical child environment and cwd (parity-harness
"shared" policy, `go/parity/README.md`), against one bench-owned sandbox so
outputs that embed data/archive paths stay byte-identical without any
normalization:

- `HOME`, `CAVE_TOOLS_DATA_DIR`, `CLAUDE_CONFIG_DIR`, `XDG_RUNTIME_DIR` → the
  run's `sandbox/` tree. Real user state, the real cache/DB and any live
  daemon are never touched; `CAVE_TOOLS_DAEMON=0` everywhere except the
  explicit `--daemon` route fixture.
- `CAVE_TOOLS_PROFILE=full`, `CAVE_TOOLS_STATUS_RTK=0`, `LC_ALL=C.UTF-8`.
- Inherited env is sanitized identically for both sides, mirroring the
  runtime's own `isSubagentContext()` detection (`src/compression/utils.ts`):
  `CAVE_TOOLS_FORCE_FRESH`, the known subagent keys (`CLINE_TASK_ID`,
  `CLAUDE_CODE_SUBAGENT`, `ANTHROPIC_SUBAGENT`, `CURSOR_TASK_ID`), any
  `TASK_ID`-style key, and the implementation switch inputs (`CAVE_TOOLS_IMPL`,
  `CAVE_TOOLS_GO_BIN`) are stripped — otherwise the dedup-stub route would be
  silently disabled. Nothing else is removed (PATH passes through); the exact
  stripped keys are recorded in `config.sanitized_env`.
- `CAVE_TOOLS_GO_DIST_DIR` points at this worktree's `dist/`. Only the Go
  runtime reads it (daemon build fingerprint, `go/internal/daemon/paths.go`);
  TS ignores it and fingerprints its own dist via `import.meta.url`. This
  keeps Go daemon sockets distinct from TS daemon sockets and from the live
  user daemon.
- Fixtures are deterministic and generated once per run (seeded, index-driven):
  `read-fixture.txt` (1,500 lines), `warmup-fixture.txt`, `compress-fixture.txt`
  (400 lines), `filter-medium.json`, `filter-large.json`. SHA-256 of every
  fixture is recorded in the results.
- Session policy: CLI routes auto-generate a per-process session; MCP routes
  use `bench-mcp-<repeat>` (fresh per repeat → first read is a dedup miss, the
  paired second read is a hit).
- Absolute entry paths: `--ts-cli` (required), `--go-src` (go module dir to
  build from, offline) or `--go-bin` (prebuilt binary).

## Validation (before timing, no normalization)

Each route runs once per side first. stdout, stderr and the exit code are
compared **byte-for-byte**. On any difference the route is *flagged*: a first
diff (byte offset + context) and SHA-256s are recorded, timing is still
collected but kept out of `summary.matched` and reported under
`summary.flagged`. Nothing is normalized or stripped to force a match. For MCP
routes the raw response line is still the gating comparison; a scoped
diagnostic `result_match` (canonical JSON of the `result` payload) separates
tool-output drift from wire-envelope drift (`envelope_only_drift` rows stay
flagged/payload-equivalent and never enter the matched summary).

Per-sample re-checks during timing (computed **outside** the captured timer):

- CLI routes: exit code + **stdout/stderr sha256** compared against that
  side's validated reference (content hashes, not lengths).
- MCP routes: canonical ToolResult payload hash + payload text length against
  that side's validated refs — never raw frame length, whose request-id digits
  vary per sample. Exception by contract: the dedup stub embeds its session
  tag and read count, so `mcp_read_dedup` is checked against the stub format
  pattern instead of a literal hash (format check, not output normalization).
  Route invariants are enforced for **both** sides:
  `mcp_read_first` must return the full read (not a stub), `mcp_read_dedup`
  must return the dedup stub. Violations mark the sample invalid with the
  invariant spelled out.
- MCP sessions are prefixed per route (`bench-first-*`, `bench-dedup-*`,
  `bench-compress-*`) so no route's cache state leaks into another's samples;
  `read_dedup` performs its paired first read untimed inside its own session.
  Both sides get untimed read + compress warmups before timing.

Deviating samples are marked `valid: false`, excluded from statistics and
listed in the JSON.

## Timing

- Default 25 repeats per side per route (`--repeats`, keep 20–30).
- Per repeat the TS/Go order is randomized by a seeded coin flip
  (`--seed`, order recorded per route as `repeat_orders`), so both sides
  interleave and neither systematically absorbs machine drift.
- Clock: `performance.now()` wall time around the whole invocation (CLI) or
  around write → complete JSON-RPC response line (MCP roundtrips).
- Reported per side and route: n, min, p50, p95, max, mean (ms), output
  bytes. Percentiles are nearest-rank (`sorted[ceil(p·n)-1]`). Raw per-sample
  values are in `timing.<route>.samples`.
- Run the final timing with no other builds/tests on the machine.

## Memory (optional `--memory`, never mixed into timings)

After timing: `/usr/bin/time -v` "Maximum resident set size", 3 runs per side,
for the CLI-process routes (CLI wrapper process only). MCP server and daemon
memory come from `/proc/<pid>/status` `VmHWM` after warmup + one validated
triple (Linux only, labeled separately in the results).

## Reproduce

```sh
# one-time, in the port worktree (uncommitted build artifacts):
ln -s /home/jlopez/AndroidStudioProjects/Per/cave-tools/node_modules node_modules
pnpm build        # worktree dist/ is the Go daemon fingerprint target (only --daemon needs it)

# final quiet run (coordinator):
node scripts/bench-ts-go.mjs \
  --ts-cli /home/jlopez/AndroidStudioProjects/Per/cave-tools/dist/cli.js \
  --go-src /home/jlopez/orca/workspaces/cave-tools/go-w3d/go \
  --repeats 25 \
  --out /tmp/opencode/w5-bench/final-results.json
```

Raw JSON results (metadata, machine/runtime/commit refs, fixture hashes,
validation, per-sample timings, stats, memory) land at `--out`. Smoke runs use
`--repeats 2`.

## Provenance (frozen paths and prebuilt binaries)

The results pin what was actually measured and refuse to pretend a prebuilt
binary came from a git HEAD:

- `ts_dist_manifest_sha256` — content fingerprint of the measured TS dist tree
  (sorted relpath + size + sha256 per `.js`); pin with
  `--expect-ts-dist-sha256`.
- `go_binary.sha256` + `go version -m` build info (toolchain, module versions,
  embedded `vcs.revision`/`vcs.time`/`vcs.modified`), plus the exact build
  command/ldflags when built from `--go-src`.
- Source ref of the Go binary: the `--go-src` repo commit when built in-run;
  the binary's own embedded `vcs.revision` (or the asserted `--go-bin-ref`
  label, e.g. `5f28d88 build job <id>`) when prebuilt — never the current
  checkout's HEAD.
- `--expect-ts-commit`, `--expect-go-commit`, `--expect-go-binary-sha256` are
  checked against the actual values; mismatches land in
  `provenance.mismatches` and `notes` (the run continues, nothing is silently
  accepted). If a prebuilt binary carries no VCS stamp and no `--go-bin-ref`
  label, the results say the source ref is unproven rather than guessing one.

## Known limitations

- TS process startup (Node.js) is inside every TS CLI sample — including any
  `--daemon` run; labels say so explicitly.
- No cold-filesystem claims; OS page cache is not reset.
- MCP routes are flagged by the byte-strict gate: the TS MCP SDK serializes
  the JSON-RPC envelope `result`-first while the Go server emits canonical
  `jsonrpc/id/result` key order (equal byte length, `result_match: true` —
  tool output identical). Timings are collected and reported under
  `summary.flagged`, never silently promoted into the matched summary.
- `daemon_cli_read` is deferred (opt-in via `--daemon`). Smoke observations
  behind that call: (1) with a shared daemon key, TS and Go daemons interop by
  design, so an unisolated run can cross-contaminate the two sides — the
  harness now pins distinct keys, but (2) daemon-served `read` dedup did not
  honor per-`--session` isolation in smoke (a second `--session` still got a
  dedup stub from the daemon's cache, and a Go-side first read returned a
  stub while TS returned full output). That is a real behavioral divergence
  worth a parity fix, not a benchmark detail; the route stays out of the core
  comparison until it is resolved.
- Archive paths in `filter` outputs are absolute; both sides share one sandbox
  data dir precisely so these stay byte-identical (not normalized away).
- Single runs carry machine noise; rerun with different `--seed` values and
  compare p50/p95 across runs before drawing conclusions.
- No hyperfine or any new tool: the harness is one dependency-free script.

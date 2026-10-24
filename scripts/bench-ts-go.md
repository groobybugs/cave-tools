# bench-ts-go — TypeScript vs Go benchmark methodology

Standalone harness: `node scripts/bench-ts-go.mjs`. Node built-ins only, no
dependencies, no new Go modules, nothing installed. The Go binary is built
locally and offline (`GOPROXY=off GOSUMDB=off`) as `cave-tools-go` (never
`cave-tools`, never installed); the TS side runs an absolute `dist/cli.js`
entry directly and is never rebuilt or modified by the harness. Both entry
paths are explicit flags; runs record source refs when available, artifact
fingerprints and explicit provenance assertions for frozen/prebuilt artifacts.

## Routes and what "fast" means for each

Core routes (default):

| route | workload | timing scope |
|---|---|---|
| `cli_help_cold` | `help` (startup + tool help load) | fresh process per sample — **TS: Node startup remains in every sample** |
| `cli_filter_medium` | `filter --json`, 50,864-byte JSON input fixture | fresh process per sample (startup + full compression pipeline) |
| `cli_filter_large` | `filter --json`, 685,296-byte JSON input fixture | same; exercises archive-handle path |
| `cli_read_cold` | `read <fixture>` | fresh process per sample = dedup miss / first-full read (file cache is in-memory per process) |
| `mcp_read_first` | `tools/call cave__read`, fresh session id | warm persistent MCP server; timed value is the tools/call roundtrip only |
| `mcp_read_dedup` | `tools/call cave__read`, same session + unchanged file | same session; dedup stub path |
| `mcp_compress` | `tools/call cave__compress` (31,922-byte text fixture) | same session |

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
  use `bench-first-*`, `bench-dedup-*` and `bench-compress-*`. First reads use
  fresh sessions; dedup calls have their own untimed first read in the same
  per-repeat session. A non-stub read uses the normal default 200-line limit,
  not the entire 121,178-byte source file.
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
  stub while TS returned full output). This remains an unresolved smoke
  observation, not proof of a Go regression: isolate daemon-key sharing,
  session scope and TS reference behavior before assigning a production fix.
  The route stays out of the core comparison until resolved.
- Archive paths in `filter` outputs are absolute; both sides share one sandbox
  data dir precisely so these stay byte-identical (not normalized away).
- Single runs carry machine noise; rerun with different `--seed` values and
  compare p50/p95 across runs before drawing conclusions.
- No hyperfine or any new tool: the harness is one dependency-free script.

## Measured baseline — 2026-10-24

Coordinator-run, 30 timed samples per side per route, two independently
interleaved orders (seeds 42 and 73), harness `106940f`. Linux x64, Intel Core
Ultra 9 275HX (24 logical CPUs), Node 24.19.0, Go 1.26.3. Desktop applications
were active; this was not a dedicated benchmark machine. No coordinator
build/test workload ran concurrently with timing. Memory and daemon phases
were off.

Frozen TS artifacts matched `main` `6cefd81` byte-for-byte. The Go binary was
built at `5f28d88` with `-trimpath -ldflags '-X main.version=0.6.1'`; its engine
matches `d163dae` (later changes were docs/provenance comments). It has no
embedded VCS stamp, so the coordinator's build log, explicit build label and
binary hash establish provenance, not a guessed checkout HEAD. Artifact pins:

- TS `.js` dist manifest SHA-256:
  `9cee457204a46424a00152dc5ff3acce09fa010dcff32df241a0c7fd62206da4`
- Go binary SHA-256:
  `cc8d06fb328885cd397a25eb23d7eb9707c5cd0b0a0a217cae5e2dd163718a28`

Seed 42 results (milliseconds). All 30 samples per side were valid.

### CLI — byte-identical stdout, stderr and exit

| Route | TS p50 | Go p50 | TS p95 | Go p95 |
| --- | ---: | ---: | ---: | ---: |
| Help / process startup | 44.1 | 3.8 | 48.1 | 4.5 |
| Medium filter | 42.2 | 19.7 | 46.5 | 21.1 |
| Large filter | 53.8 | 169.6 | 56.4 | 177.4 |
| First read, default limit | 44.5 | 4.8 | 50.4 | 5.1 |

### Warm MCP — payload-equivalent, envelope bytes differ

These remain in `summary.flagged`, not `summary.matched`: JSON-RPC key order
differs. Canonical ToolResult payload hashes matched across TS/Go for every
paired sample, including dynamic dedup stubs. Both sides satisfied full-read
versus stub invariants. No content text was normalized.

| Route | TS p50 | Go p50 | TS p95 | Go p95 |
| --- | ---: | ---: | ---: | ---: |
| First read, default limit | 0.9 | 0.7 | 3.1 | 1.5 |
| Cached unchanged read | 0.4 | 0.2 | 1.1 | 0.8 |
| Compress text | 0.5 | 2.6 | 0.8 | 3.2 |

Seed 73 reproduced the direction: large filter TS/Go p50 52.0/170.9 ms,
MCP compression 0.6/2.6 ms. Cached-read medians were both about 0.4 ms; do not
claim a robust cached-read speedup from sub-millisecond noise. First-versus-last
10-sample medians did not show accumulating large-filter latency in either run.

A separate Go CPU/allocation profile of the same large-filter pipeline (30
iterations, no process startup) measured 157.6 ms/op and 10.7 MB allocated per
call (not RSS). `RedactSecrets` accounted for 82.6% of sampled CPU; regex
execution overall 95.9%, `ApplyBudget` 13.8%, and `ArchiveHandle` 1.2%.
Cumulative percentages overlap. The measured hotspot is repeated regex
matching/case folding, not primarily SQLite or startup. No production
optimization had been made at that baseline; the verified follow-up is below.
Redaction must not be disabled or moved after truncation to improve numbers.

Raw JSON, provenance, CPU/memory profiles and the temporary profiling harness
are retained locally under `.context/go-port-benchmarks/2026-10-24/` (not shipped
as package files). This is workload-specific evidence, not a global Go speedup,
extra token-savings claim, or full migration acceptance.

## Redaction optimization — verified follow-up

User approved a targeted redaction optimization after the baseline profile.
Candidate `662e77b` preserves all regex sources, flags, rule order, replacements
and per-match counter semantics. Mandatory-prefix checks reject impossible
matches; regex execution starts at the earliest possible match. Non-ASCII
case-fold inputs fall back to the original regex path. No redaction is disabled
or delayed until after truncation.

Muse reviewed the security proof and the copied pre-optimization differential
oracle clean. Tests cover all 13 rules, whitespace, Unicode folding, invalid
UTF-8, overlap/order, late large-input secrets, idempotency and match counts.
Existing Go-vs-JS non-`u` case-fold differences are preserved, not changed by
this optimization.

Coordinator re-ran all seven core routes, 30 samples per side per route,
seeds 42 and 73. Each seed ran the old Go control and then the candidate against
the same frozen TS artifact. All samples were valid, artifact pins matched,
four CLI routes matched bytes, and every paired MCP ToolResult payload hash
matched (envelope-only differences remain flagged). Lower times are better:

| Workload / seed | Old Go p50 | New Go p50 | TS p50 in candidate run | Old Go p95 | New Go p95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Large filter / 42 | 172.2 | 49.9 | 53.4 | 185.2 | 51.7 |
| Large filter / 73 | 170.2 | 50.1 | 52.3 | 177.3 | 54.5 |
| Medium filter / 42 | 19.9 | 8.2 | 43.4 | 20.7 | 9.5 |
| Medium filter / 73 | 19.6 | 8.1 | 41.9 | 20.9 | 8.8 |

Times are milliseconds. Large filtering improved about **3.4x** (about 71%
lower median latency), reaching roughly TS speed on this fixture. Do not turn
the small TS/Go difference into a universal Go-faster claim. Help/read startup
remained fast. Warm MCP compression remained about 2.4–2.6 ms in Go versus
0.5 ms in TS; that separate hot path was not optimized here.

A separate coordinator microbenchmark of the optimized redactor, 10 iterations,
measured 8.54 ms/op on a clean ~650 KiB payload and 29.45 ms/op with secrets at
the start, middle and end. This is not a pipeline/RSS measurement or an
independently reproduced baseline microbenchmark comparison. The secret case
allocated about 2.53 MB/op; the clean case allocated zero bytes per operation.

Candidate binary SHA-256:
`55acc144ea170b9390c4b7dd2fcb0656d7b160df67f0e827162262cf3b3d386e`.
Build flags/toolchain matched the old control (`-trimpath`, version 0.6.1,
Go 1.26.3). Raw old/new reports, build metadata, microbenchmark stdout and the
pre-registered plan are retained locally under
`.context/go-port-benchmarks/2026-10-24/` (optimization subdirectory). The three
unrelated expanded-W5 parity failures remain outside this change.

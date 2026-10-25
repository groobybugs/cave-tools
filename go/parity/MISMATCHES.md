# W5 parity status

Environment: TypeScript reference from `dist/` (node 24.19.0, ICU 78.3,
Unicode 17.0) against the Go binary built by the harness. All cases live in
`go/parity/` behind the `parity` build tag and run with
`go test -tags parity ./parity/`. This file records the final W5 state: every
mismatch found during W5 is fixed and pinned by a parity or differential case,
and the remaining accepted deviations are listed below. No parity case was
edited or weakened while fixing them.

## Fixed W5 mismatches

| Mismatch | Fixed in | Test |
| --- | --- | --- |
| Missing-path suggestions kept filesystem order instead of Node `readdir`'s sorted order (`read .txt` picked the wrong three names). | `68e8835` | `TestCLIBlackBoxDifferential/read.missing-multi-suggestion-order` |
| An empty `cave__bash` command ran the shell and exited 0; Node `spawn` rejects it (`Command failed to launch`). | `9cabc7a` | `TestCLIBlackBoxDifferential/bash.empty-command` |
| `filter --json` printed `encoding/json` syntax errors instead of V8's `Unexpected token … is not valid JSON` text. | `8b8adef` | `TestCLIBlackBoxDifferential/filter.invalid-json` |
| Invalid UTF-8 on stdin was replaced per byte run (Go) instead of per maximal invalid subpart (Node `StringDecoder`); out-of-range numbers (`1e400`, 400-digit integers) were rejected instead of parsing to ±Infinity. | `db43715` | `TestCLIBlackBoxDifferential/filter.*`, `internal/cli/testdata/decoder_cases.json`, `filter_cases.json` |
| Suggestions/hash previews used Go `strings.ToLower`; JS `toLowerCase` differs on U+0130 and on Unicode 16/17 case pairs, and implements Final_Sigma. | `3f5f54d` | `internal/jsre` node fixtures + read/edit differential cases |
| `expected_hash` minimum-length gate used UTF-8 bytes instead of UTF-16 code units, changing which error non-ASCII input got. | `423b2b3` | edit unit + differential cases |
| CLI array/object flags rejected `1e400` while `JSON.parse` accepts it (and maps it to Infinity). | `b4c0604` | `TestCLIOverflowNumbersMatchNode` (`internal/cli/testdata/overflow_cases.json`) |
| Daemon raw request args dropped `1e400`; the Go client wrote Infinity/NaN where `JSON.stringify` writes `null`. | `e5e8ed2` | `TestServerArgsNumberOverflow` |
| MCP `tools/call` arguments were dropped or rejected on out-of-range numbers before dispatch. | `ce8c61b` | `TestDecodeCallArgumentsNumberOverflow`, `TestMCPNumericOverflowCasesMatchNode` |
| `websearch` provider payloads with out-of-range numbers returned "no payload". | `a076679` | `TestParseWebsearchResponseNumberOverflow` |

The three W5a cases that failed on the pre-fix base
(`read.missing-multi-suggestion-order`, `bash.empty-command`,
`filter.invalid-json`) are all fixed; on this base the parity suite is green.

## Accepted deviations

These are verified against the code and tests and are deliberately not
normalized into the suite. They are limits, not parity claims.

1. **Lone surrogates.** A `\uD800`-style escape decodes to U+FFFD in Go because
   Go strings cannot hold a lone code unit; `JSON.parse` keeps the surrogate.
   Exit codes agree and CLI text output converges on U+FFFD; JSON output does
   not (Go emits U+FFFD, TS re-emits the escape). This includes `jsjson`
   `\u` escapes and a surrogate pair split by `utf16Slice`.
2. **JSON nesting cap.** `encoding/json` rejects documents nested deeper than
   10000 levels; V8 `JSON.parse` accepts deeper input (11000 levels is valid in
   this environment). Pathological depth only; both sides agree through 9000
   levels.
3. **Node-version pinning.** JS-behaviour fixtures are generated with node
   24.19.0 (ICU 78.3, Unicode 17.0): `internal/jsre/testdata/lower_node.txt`,
   `lower_sequences.txt`, the JSON/whitespace expectations in
   `internal/jsre/jsre_test.go`, and the node-recorded JSON fixtures under
   `internal/*/testdata/`. A different Node changes the fixtures, not the
   implementation.
4. **MCP `params.name` protocol text.** A missing, null or non-string
   `params.name` is decoded by the Go SDK's typed request parameters, so the
   protocol error text differs from the TS SDK's zod text. Valid names, tool
   schemas, argument-record validation and all tool results match.
5. **JSON-RPC envelope key order.** The Go SDK can order `jsonrpc`/`id`/`result`
   differently from the TS SDK's insertion order. Every value matches; the wire
   differential test compares decoded frames.
6. **Unknown daemon content blocks.** The Go daemon client models text, image
   and resource blocks only; an unknown MCP content-block type in a daemon
   reply is reported as connection-lost (never retried) instead of passed
   through. No cave-tools tool emits one.
7. **Standalone binary daemon key.** A standalone Go binary run outside the
   repository with no `CAVE_TOOLS_GO_DIST_DIR` cannot derive the dist-based
   daemon key and stays in-process.
8. **Go-native web gaps, not on the routed path.** Under the opt-in switch
   `webfetch`/`websearch` stay in TypeScript, so these only apply when the Go
   binary is invoked directly: no Brotli and no zstd decoding (and no new Go
   dependency is allowed), stacked `Content-Encoding` is not decoded, and the
   pre-decode `Content-Length`/gzip-trailing-junk behaviour is Go-native. See
   `docs/go-port.md` "Web transport".
9. **Windows is TODO for runtime acceptance.** Only `GOOS=windows go vet ./...`
   is claimed; Windows signals, named-pipe daemon transport and cmd.exe
   behavior are not runtime-verified here. See `docs/go-port.md`.

## Not exercised (reported, not waived)

- `websearch`/`webfetch` success paths need network; only usage errors and the
  non-http scheme rejection are covered.
- `bins` download path: only the fake-rg/fd fixture is exercised.
- Daemon malformed reply lines, idle in-flight shutdown, retry/backoff and
  request-drop-never-reruns are covered by package tests
  (`internal/daemon`), not duplicated as black-box rows.
- `bash_start` output is nondeterministic (job id/time); its argv mapping and
  durable behavior are covered by cross-implementation job tests.

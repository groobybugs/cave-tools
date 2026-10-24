# Known parity gap: Brotli (`Content-Encoding: br`)

**Status: UNRESOLVED — coordinator decision required.** No new dependency is
allowed in this wave, and Go's standard library has no Brotli decoder.

Both `cave__webfetch` and `cave__websearch` call Node's `fetch` in TypeScript.
Node/undici decodes a Brotli response body even when the request did not
advertise `br`. Go can decode `gzip` and both `deflate` framings with the
standard library, but not Brotli.

Request negotiation was aligned with Node fetch and with what Go can decode:
both tools now send `Accept-Encoding: gzip, deflate`. `br` is deliberately not
advertised.

## Forced-Brotli repro

Fixture: `brotliCompressSync(Buffer.from("hello br"))` from Node,
hex `8b038068656c6c6f20627203`, served with `content-type: text/plain` and
`content-encoding: br`.

Exact outputs from the parity run:

- TypeScript (`webfetch`, `format: "text"`):
  `{"content":[{"type":"text","text":"hello br"}]}`
- Go:
  `{"content":[{"type":"text","text":"\uFFFD\u0003\uFFFDhello br\u0003"}]}`
  (the raw Brotli bytes are passed through the UTF-8 decoder)

Command:

```
cd go && go test -tags parity -run TestParityBrotliKnownGap -v ./internal/tools/webfetch/
```

`TestParityBrotliKnownGap` asserts this exact difference and logs both outputs.
It fails if Go ever decodes Brotli so this document and the test get updated.
The rest of the parity suite is Brotli-free and green; this is not a claim of
Brotli parity.

Resolution options for the coordinator: approve a Brotli dependency
(`github.com/andybalholm/brotli` or equivalent) for both tool packages, or
accept this documented divergence and keep `br` out of request negotiation.

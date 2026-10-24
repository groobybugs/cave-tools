// Package parity compares the Go port of cave-tools against the TypeScript
// reference implementation.
//
// The harness itself lives in parity_test.go behind the `parity` build tag, so
// a plain `go test ./...` compiles this package but runs no binary comparisons.
// Run it with:
//
//	pnpm build && cd go && go test -tags parity ./parity/
package parity

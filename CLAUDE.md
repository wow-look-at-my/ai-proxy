# ai-proxy

- Build and test: `go-toolchain` in the repo root. Tests are `_test.go` with in-process HTTP servers. No test may reach the network.
- The relay never parses a request or response body. A feature that needs to read a field belongs in another tool. Keep it that way: it is why every client feature works through the relay unchanged.
- `zdelta` is the wire protocol, shared with Go clients. A change to a header name or to the 409 shape is a breaking change for the grok-build client. Update `docs/delta-protocol.md` in the same commit.
- The zstd CLI wrote `zdelta/testdata/*.zst` (libzstd). It proves the Go decoder reads what a libzstd client writes. Regenerate it with `zstd --patch-from`, not with the Go encoder.
- One cobra subcommand per file in `cmd/`, each registering itself in `init`.

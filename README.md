# ai-proxy

A relay for LLM APIs that makes a slow link cheap to use. Run it on a host close to the upstream. Point your client at it.

It forwards every request byte for byte. It never parses a body, so any field, header or dialect the client and upstream agree on works through it. It only undoes the compression the client applied:

- **zstd bodies.** Every response says `Accept-Encoding: zstd`. A client can compress its uploads.
- **zstd deltas.** A client can encode a request against the one. The upload is then roughly the size of the new turn, not the whole history.

## Run

```sh
ai-proxy serve --listen :8317 \
  --route anthropic=https://api.anthropic.com \
  --route openai=https://api.openai.com
```

A client then uses `http://host:8317/anthropic/v1` as its Anthropic base URL. Set `--token` (or `$AI_PROXY_TOKEN`) and send it in `X-AI-Proxy-Token` to keep strangers off the port.

## Delta protocol

[docs/delta-protocol.md](docs/delta-protocol.md) describes the headers a client sends. Package `zdelta` holds the Go encoder.

## License

MIT, see [LICENSE](LICENSE).

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

A client then uses `http://host:8317/anthropic/v1` as its Anthropic base URL and sends its own upstream key.

To keep strangers off the port, set `--token` (or `$AI_PROXY_TOKEN`). A client then uses the token as its API key, and the relay swaps in the real key:

```sh
AI_PROXY_TOKEN=relay-secret \
AI_PROXY_UPSTREAM_KEY_ANTHROPIC=sk-ant-... \
ai-proxy serve --route anthropic=https://api.anthropic.com
```

```sh
ANTHROPIC_BASE_URL=http://host:8317/anthropic ANTHROPIC_API_KEY=relay-secret claude
```

The token works. `--upstream-key name=KEY` also sets a key. A client that sends its own upstream key can put the token in `X-AI-Proxy-Token` instead.

## Delta protocol

[docs/delta-protocol.md](docs/delta-protocol.md) describes the headers a client sends. Package `zdelta` holds the Go encoder.

## License

MIT, see [LICENSE](LICENSE).

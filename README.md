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

To keep strangers off the port, set `--token` (or `$AI_PROXY_TOKEN`). The relay token and the upstream key are then separate secrets. The client sends the token in `X-AI-Proxy-Token`, and its upstream key in the usual header. The relay checks and strips the token, and passes the key through untouched.

Claude Code sends the extra header through `ANTHROPIC_CUSTOM_HEADERS`:

```sh
ANTHROPIC_BASE_URL=http://host:8317/anthropic \
ANTHROPIC_API_KEY=sk-ant-... \
ANTHROPIC_CUSTOM_HEADERS='X-AI-Proxy-Token: relay-secret' \
claude
```

## Check that it works

The relay logs one line on stderr for each request it answers:

```
level=INFO msg=request method=POST path=/anthropic/v1/messages status=200 upload=plain wire_bytes=16026 response_bytes=11 duration=10ms
level=INFO msg=request method=POST path=/anthropic/v1/messages status=200 upload=zstd wire_bytes=76 plain_bytes=16026 response_bytes=11 duration=1ms
level=INFO msg=request method=POST path=/anthropic/v1/messages status=200 upload=delta wire_bytes=36 plain_bytes=16065 response_bytes=11 duration=1ms
level=WARN msg=request method=POST path=/nowhere/v1/messages status=404 upload=plain wire_bytes=16026 response_bytes=97 duration=0s
```

`wire_bytes` against `plain_bytes` shows what the link saved. A line with an error status logs at `WARN`. No line means the client did not reach the relay.

## Updates

The relay serves the [docker-updater](https://github.com/wow-look-at-my/docker-updater) checks under `/.well-known/docker-updater/`. They need no token. `pre-update` holds an update while a request is in flight. The relay logs `update held` or `update allowed` for each answer.

On `SIGTERM` the relay takes no new connection and finishes the requests in flight. It logs `draining`, then `drained`. The stop timeout of the container runtime is the limit.

## Delta protocol

[docs/delta-protocol.md](docs/delta-protocol.md) describes the headers a client sends. Package `zdelta` holds the Go encoder.

## License

MIT, see [LICENSE](LICENSE).

# Delta protocol

A client that knows the relay holds its previous request body uploads only what changed. All of it rides HTTP headers. The body is ordinary zstd.

## What the relay sends

Every response, errors included, carries:

```
Accept-Encoding: zstd
X-Zstd-Delta: sha256
```

`X-Zstd-Delta` is absent when the relay runs with `--delta-cache-mib 0`.

## What the client sends

```
Content-Encoding: zstd
X-Zstd-Delta: sha256            # keep the decoded body as a future base
X-Zstd-Base: <sha256 hex>       # the body this one was encoded against
```

- The body is one zstd frame. With `X-Zstd-Base` it is encoded with the base body as a raw dictionary. In libzstd that is `ZSTD_CCtx_refPrefix` or `zstd --patch-from=<base>`. In Go it is `zdelta.Encode(next, base)`.
- The hash is SHA-256 of the plain base bytes, lowercase hex.
- The window must cover the base plus the new body. libzstd sets it for `--patch-from`. A raw libzstd client sets `ZSTD_c_windowLog` itself and turns on long-distance matching for bodies over a few MiB.
- The relay accepts a window up to twice `--max-body-mib`. A client skips the delta when the base plus the body is larger, and sends a full frame.

## The relay's answer to an unknown base

```
HTTP/1.1 409 Conflict
X-Zstd-Base-Missing: 1

{"type":"error","error":{"type":"zstd_base_missing","message":"..."}}
```

The relay keeps bases in a byte-bounded LRU in memory, so a restart or eviction loses them. The client sends the same request again without `X-Zstd-Base`, still zstd-compressed. Nothing reaches the upstream on a 409.

## Limits

`--max-body-mib` caps the encoded body and the decoded body. A frame that decodes past it gets a 413.

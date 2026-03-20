# TLTV Examples

Minimal examples for the [TLTV Federation Protocol](https://spec.timelooptv.org).
Small, single-file implementations to help you understand the protocol and start building.

## Channel Servers

A channel server is the simplest TLTV node -- it generates a keypair, signs
metadata, and serves an HLS stream. Each example below is a complete,
conforming TLTV v1 channel in a single file.

| Example | Runtime | External Deps | Size |
|---------|---------|---------------|------|
| [server/node/](server/node/) | Node.js 18+ | None | ~90 lines |
| [server/python/](server/python/) | Python 3.9+ | `cryptography` | ~80 lines |
| [server/go/](server/go/) | Go 1.22+ | None | ~130 lines |

### Quick Start

```bash
# 1. Generate test HLS content (requires ffmpeg)
server/generate-stream.sh server/node/media

# 2. Run a server
cd server/node && node server.mjs

# 3. Optionally verify with tltv-cli (https://github.com/tltv-org/cli)
tltv node localhost:8000 --local
tltv fetch TV...@localhost:8000 --local
tltv stream TV...@localhost:8000 --local
```

### Running Each Example

**Node.js** (zero dependencies):
```bash
cd server/node && node server.mjs
```

**Python** (one dependency):
```bash
pip install cryptography
cd server/python && python server.py
```

**Go** (zero dependencies):
```bash
cd server/go && go run main.go
```

### What Each Server Implements

Every example is a conforming TLTV v1 node:

- Generates an Ed25519 keypair on first run (saved to `channel.key`)
- `GET /.well-known/tltv` -- Node discovery info
- `GET /tltv/v1/channels/{id}` -- Signed channel metadata
- `GET /tltv/v1/channels/{id}/stream.m3u8` -- HLS manifest
- `GET /tltv/v1/channels/{id}/*.ts` -- HLS segments
- `GET /tltv/v1/peers` -- Peer exchange (empty list)
- CORS headers and JSON error responses on all endpoints

### Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `PORT` | `8000` | Listen port |
| `CHANNEL_NAME` | *(varies)* | Channel display name |
| `MEDIA_DIR` | `./media` | Directory containing HLS files |

### Test Stream

Generate a 30-second test pattern with ffmpeg:

```bash
server/generate-stream.sh <example>/media
```

Without a stream, the server still works -- metadata and node info are served
normally, and the stream endpoint returns `503 stream_unavailable`.

## Key Files

All examples store keys as hex-encoded 32-byte Ed25519 seeds in `channel.key`.
This is the same format `tltv-cli keygen` produces, so keys are interchangeable.

## Links

- [Protocol Spec](https://spec.timelooptv.org) -- Full TLTV v1 specification
- [tltv-cli](https://github.com/tltv-org/cli) -- CLI for interacting with the network

## License

MIT -- see [LICENSE](LICENSE).

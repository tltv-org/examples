#!/usr/bin/env python3
"""Minimal TLTV node — Python 3.9+, single dependency: pip install cryptography"""

import json, os, time
from datetime import datetime, timezone
from http.server import HTTPServer, BaseHTTPRequestHandler
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import (
    Encoding,
    PublicFormat,
    PrivateFormat,
    NoEncryption,
)

PORT = int(os.environ.get("PORT", "8000"))
NAME = os.environ.get("CHANNEL_NAME", "Python TLTV Channel")
MEDIA = os.environ.get("MEDIA_DIR", "./media")

# Base58 (Bitcoin alphabet)
B58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"


def b58encode(data: bytes) -> str:
    n = int.from_bytes(data, "big")
    s = ""
    while n > 0:
        n, r = divmod(n, 58)
        s = B58[r] + s
    return "1" * (len(data) - len(data.lstrip(b"\0"))) + s


def load_or_create_key(path):
    if os.path.exists(path):
        return Ed25519PrivateKey.from_private_bytes(
            bytes.fromhex(open(path).read().strip())
        )
    key = Ed25519PrivateKey.generate()
    seed = key.private_bytes(Encoding.Raw, PrivateFormat.Raw, NoEncryption())
    with open(path, "w") as f:
        f.write(seed.hex() + "\n")
    os.chmod(path, 0o600)
    print(f"Generated new keypair -> {path}")
    return key


key = load_or_create_key("channel.key")
pub = key.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw)
ID = b58encode(b"\x14\x33" + pub)


def sign_doc(doc):
    clean = {k: v for k, v in doc.items() if k != "signature"}
    doc["signature"] = b58encode(
        key.sign(json.dumps(clean, sort_keys=True, separators=(",", ":")).encode())
    )
    return doc


def metadata():
    now = datetime.now(timezone.utc)
    return sign_doc(
        {
            "v": 1,
            "seq": int(now.timestamp()),
            "id": ID,
            "name": NAME,
            "stream": f"/tltv/v1/channels/{ID}/stream.m3u8",
            "updated": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        }
    )


PFX = f"/tltv/v1/channels/{ID}"


class Handler(BaseHTTPRequestHandler):
    def do_OPTIONS(self):
        self.send_response(204)
        self._cors()
        self.end_headers()

    def do_GET(self):
        path = self.path.split("?")[0]
        if path == "/.well-known/tltv":
            self._json(
                {
                    "protocol": "tltv",
                    "versions": [1],
                    "channels": [{"id": ID, "name": NAME}],
                    "relaying": [],
                }
            )
        elif path == PFX:
            self._json(metadata())
        elif path == PFX + "/stream.m3u8":
            self._file(
                "stream.m3u8", "application/vnd.apple.mpegurl", "max-age=1, no-cache"
            )
        elif path.startswith(PFX + "/") and path.endswith(".ts"):
            self._file(os.path.basename(path), "video/mp2t", "max-age=3600")
        elif path == "/tltv/v1/peers":
            self._json({"peers": []})
        else:
            self._json({"error": "channel_not_found"}, 404)

    def _json(self, data, status=200):
        body = json.dumps(data).encode()
        self.send_response(status)
        self._cors()
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.end_headers()
        self.wfile.write(body)

    def _file(self, name, ct, cc):
        path = os.path.join(MEDIA, name)
        if not os.path.isfile(path):
            return self._json({"error": "stream_unavailable"}, 503)
        self.send_response(200)
        self._cors()
        self.send_header("Content-Type", ct)
        self.send_header("Cache-Control", cc)
        self.end_headers()
        self.wfile.write(open(path, "rb").read())

    def _cors(self):
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, OPTIONS")

    def log_message(self, *_):
        pass


print(
    f"Channel: {ID}\nListen:  :{PORT}\nURI:     tltv://{ID}@localhost:{PORT}\n\nVerify:  tltv fetch {ID}@localhost:{PORT}"
)
HTTPServer(("", PORT), Handler).serve_forever()

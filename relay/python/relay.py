#!/usr/bin/env python3
"""Minimal TLTV relay — Python 3.9+, single dependency: pip install cryptography"""

import json, os, re, threading, time, urllib.request
from http.server import HTTPServer, BaseHTTPRequestHandler
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from cryptography.exceptions import InvalidSignature

PORT = int(os.environ.get("PORT", "9000"))
UPSTREAM = os.environ.get("UPSTREAM", "localhost:8000")
BASE = f"http://{UPSTREAM}"

# Base58 (Bitcoin alphabet) — decode needed for signature verification
B58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
B58MAP = {c: i for i, c in enumerate(B58)}


def b58decode(s: str) -> bytes:
    n = 0
    for c in s:
        n = n * 58 + B58MAP[c]
    data = n.to_bytes((n.bit_length() + 7) // 8, "big") if n else b""
    return b"\x00" * (len(s) - len(s.lstrip("1"))) + data


def verify_doc(doc: dict) -> bool:
    """Verify Ed25519 signature on a TLTV metadata document — no private key needed."""
    sig, cid = doc.get("signature"), doc.get("id")
    if not sig or not cid:
        return False
    try:
        raw = b58decode(cid)
        if len(raw) < 34 or raw[0] != 0x14 or raw[1] != 0x33:
            return False
        pub = Ed25519PublicKey.from_public_bytes(raw[2:])
        body = {k: v for k, v in doc.items() if k != "signature"}
        canonical = json.dumps(body, sort_keys=True, separators=(",", ":")).encode()
        pub.verify(b58decode(sig), canonical)
        return True
    except (InvalidSignature, Exception):
        return False


def fetch_bytes(path: str) -> bytes:
    return urllib.request.urlopen(f"{BASE}{path}", timeout=5).read()


def fetch_json(path: str):
    return json.loads(fetch_bytes(path))


# Relay state
lock = threading.Lock()
channels = {}  # id -> {meta, manifest, segs: {name: bytes}}


def sync_meta():
    try:
        info = fetch_json("/.well-known/tltv")
        seen = set()
        for ch in [*info.get("channels", []), *info.get("relaying", [])]:
            try:
                meta = fetch_json(f"/tltv/v1/channels/{ch['id']}")
                if not verify_doc(meta):
                    continue
                if meta.get("access") == "token" or meta.get("on_demand"):
                    continue
                cid = ch["id"]
                seen.add(cid)
                with lock:
                    if cid not in channels:
                        channels[cid] = {"meta": meta, "manifest": None, "segs": {}}
                        print(f"  + {meta['name']} ({cid})")
                    elif meta["seq"] > channels[cid]["meta"]["seq"]:
                        if meta.get("status") == "retired":
                            del channels[cid]
                        else:
                            channels[cid]["meta"] = meta
            except Exception:
                pass
        with lock:
            for cid in list(channels):
                if cid not in seen:
                    del channels[cid]
    except Exception as e:
        print(f"  sync: {e}")


def sync_hls():
    with lock:
        items = list(channels.items())
    for cid, ch in items:
        try:
            manifest = fetch_bytes(f"/tltv/v1/channels/{cid}/stream.m3u8")
            names = set()
            for line in manifest.decode().splitlines():
                name = line.strip()
                if name and not name.startswith("#"):
                    names.add(name)
                    if name not in ch["segs"]:
                        try:
                            ch["segs"][name] = fetch_bytes(
                                f"/tltv/v1/channels/{cid}/{name}"
                            )
                        except Exception:
                            pass
            with lock:
                ch["manifest"] = manifest
                for k in list(ch["segs"]):
                    if k not in names:
                        del ch["segs"][k]
        except Exception:
            pass


def poll():
    while True:
        sync_meta()
        for _ in range(30):
            sync_hls()
            time.sleep(2)


# HTTP server
CHAN_RE = re.compile(r"^/tltv/v1/channels/([^/]+)$")
M3U8_RE = re.compile(r"^/tltv/v1/channels/([^/]+)/stream\.m3u8$")
SEG_RE = re.compile(r"^/tltv/v1/channels/([^/]+)/(.+\.ts)$")


class Handler(BaseHTTPRequestHandler):
    def do_OPTIONS(self):
        self.send_response(204)
        self._cors()
        self.end_headers()

    def do_GET(self):
        path = self.path.split("?")[0]

        if path == "/.well-known/tltv":
            with lock:
                relaying = [
                    {"id": c["meta"]["id"], "name": c["meta"]["name"]}
                    for c in channels.values()
                ]
            return self._json(
                {
                    "protocol": "tltv",
                    "versions": [1],
                    "channels": [],
                    "relaying": relaying,
                }
            )

        if path == "/tltv/v1/peers":
            return self._json({"peers": []})

        m = CHAN_RE.match(path)
        if m:
            with lock:
                ch = channels.get(m.group(1))
            if not ch:
                return self._json({"error": "channel_not_found"}, 404)
            return self._json(ch["meta"])

        m = M3U8_RE.match(path)
        if m:
            with lock:
                ch = channels.get(m.group(1))
                manifest = ch["manifest"] if ch else None
            if not ch:
                return self._json({"error": "channel_not_found"}, 404)
            if not manifest:
                return self._json({"error": "stream_unavailable"}, 503)
            self.send_response(200)
            self._cors()
            self.send_header("Content-Type", "application/vnd.apple.mpegurl")
            self.send_header("Cache-Control", "max-age=1, no-cache")
            self.end_headers()
            return self.wfile.write(manifest)

        m = SEG_RE.match(path)
        if m:
            with lock:
                ch = channels.get(m.group(1))
                seg = ch["segs"].get(m.group(2)) if ch else None
            if not ch:
                return self._json({"error": "channel_not_found"}, 404)
            if not seg:
                return self._json({"error": "stream_unavailable"}, 503)
            self.send_response(200)
            self._cors()
            self.send_header("Content-Type", "video/mp2t")
            self.send_header("Cache-Control", "max-age=3600")
            self.end_headers()
            return self.wfile.write(seg)

        self._json({"error": "invalid_request"}, 400)

    def _json(self, data, status=200):
        body = json.dumps(data).encode()
        self.send_response(status)
        self._cors()
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.end_headers()
        self.wfile.write(body)

    def _cors(self):
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, OPTIONS")

    def log_message(self, *_):
        pass


print(
    f"TLTV Relay  upstream={UPSTREAM}\nListen:  :{PORT}\n\nVerify:  tltv node localhost:{PORT} --local"
)
threading.Thread(target=poll, daemon=True).start()
HTTPServer(("", PORT), Handler).serve_forever()

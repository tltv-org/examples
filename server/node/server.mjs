// Minimal TLTV node — Node.js 18+, zero dependencies
// Usage: node server.mjs

import { createServer } from 'node:http'
import { generateKeyPairSync, sign, createPrivateKey, createPublicKey } from 'node:crypto'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'

const PORT = parseInt(process.env.PORT || '8000')
const NAME = process.env.CHANNEL_NAME || 'Node.js TLTV Channel'
const MEDIA = process.env.MEDIA_DIR || './media'

// Base58 (Bitcoin alphabet)
const B58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz'
function b58encode(buf) {
  let n = buf.reduce((a, b) => a * 256n + BigInt(b), 0n)
  let s = ''
  while (n > 0n) { s = B58[Number(n % 58n)] + s; n /= 58n }
  for (const b of buf) { if (b) break; s = '1' + s }
  return s
}

// RFC 8785 canonical JSON (handles TLTV types: strings, ints, bools, arrays, objects)
function cjson(v) {
  if (typeof v === 'string') return JSON.stringify(v)
  if (typeof v !== 'object' || v === null) return String(v)
  if (Array.isArray(v)) return '[' + v.map(cjson).join(',') + ']'
  return '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ':' + cjson(v[k])).join(',') + '}'
}

// Ed25519 key management — stores hex-encoded seed (tltv-cli compatible)
const PKCS8_PREFIX = Buffer.from('302e020100300506032b657004220420', 'hex')
function loadOrCreateKey(path) {
  let priv
  if (existsSync(path)) {
    const seed = Buffer.from(readFileSync(path, 'utf8').trim(), 'hex')
    priv = createPrivateKey({ key: Buffer.concat([PKCS8_PREFIX, seed]), format: 'der', type: 'pkcs8' })
  } else {
    priv = generateKeyPairSync('ed25519').privateKey
    const der = priv.export({ type: 'pkcs8', format: 'der' })
    writeFileSync(path, Buffer.from(der.subarray(16)).toString('hex') + '\n', { mode: 0o600 })
    console.log('Generated new keypair ->', path)
  }
  const pub = createPublicKey(priv).export({ type: 'spki', format: 'der' })
  return { priv, pub: Buffer.from(pub.subarray(pub.length - 32)) }
}

const { priv, pub } = loadOrCreateKey('channel.key')
const ID = b58encode(Buffer.concat([Buffer.from([0x14, 0x33]), pub]))

function signDoc(doc) {
  const { signature, ...clean } = doc
  doc.signature = b58encode(sign(null, Buffer.from(cjson(clean)), priv))
  return doc
}

function metadata() {
  const now = new Date()
  return signDoc({
    v: 1, seq: Math.floor(now.getTime() / 1000),
    id: ID, name: NAME,
    stream: `/tltv/v1/channels/${ID}/stream.m3u8`,
    updated: now.toISOString().replace(/\.\d+Z$/, 'Z'),
  })
}

// HTTP helpers
function json(res, data, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(data))
}

function serveFile(res, path, ct, cc) {
  try {
    res.writeHead(200, { 'Content-Type': ct, 'Cache-Control': cc })
    res.end(readFileSync(path))
  } catch { json(res, { error: 'stream_unavailable' }, 503) }
}

const pfx = `/tltv/v1/channels/${ID}`
createServer((req, res) => {
  res.setHeader('Access-Control-Allow-Origin', '*')
  res.setHeader('Access-Control-Allow-Methods', 'GET, OPTIONS')
  if (req.method === 'OPTIONS') { res.writeHead(204); return res.end() }
  if (req.method !== 'GET') { return json(res, { error: 'invalid_request' }, 400) }

  const path = new URL(req.url, `http://${req.headers.host}`).pathname

  if (path === '/.well-known/tltv')
    return json(res, { protocol: 'tltv', versions: [1], channels: [{ id: ID, name: NAME }], relaying: [] })
  if (path === pfx) return json(res, metadata())
  if (path === pfx + '/stream.m3u8')
    return serveFile(res, join(MEDIA, 'stream.m3u8'), 'application/vnd.apple.mpegurl', 'max-age=1, no-cache')
  if (path.startsWith(pfx + '/') && path.endsWith('.ts')) {
    const file = path.slice(pfx.length + 1)
    if (!file.includes('/')) return serveFile(res, join(MEDIA, file), 'video/mp2t', 'max-age=3600')
  }
  if (path === '/tltv/v1/peers') return json(res, { peers: [] })
  json(res, { error: 'channel_not_found' }, 404)
}).listen(PORT, () => {
  console.log(`Channel: ${ID}`)
  console.log(`Listen:  :${PORT}`)
  console.log(`URI:     tltv://${ID}@localhost:${PORT}`)
  console.log(`\nVerify:  tltv fetch ${ID}@localhost:${PORT}`)
})

// Minimal TLTV relay — Node.js 18+, zero dependencies
// Usage: UPSTREAM=localhost:8000 node relay.mjs

import { createServer } from 'node:http'
import { verify, createPublicKey } from 'node:crypto'

const PORT = parseInt(process.env.PORT || '9000')
const UPSTREAM = process.env.UPSTREAM || 'localhost:8000'
const UP = `http://${UPSTREAM}`

// Base58 (Bitcoin alphabet) — decode needed for signature verification
const B58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz'
const B58MAP = Object.fromEntries([...B58].map((c, i) => [c, BigInt(i)]))

function b58decode(s) {
  let n = 0n
  for (const c of s) n = n * 58n + B58MAP[c]
  const hex = n === 0n ? '' : n.toString(16)
  const padded = hex.length % 2 ? '0' + hex : hex
  const bytes = Buffer.from(padded, 'hex')
  let z = 0
  while (z < s.length && s[z] === '1') z++
  return z ? Buffer.concat([Buffer.alloc(z), bytes]) : bytes
}

// RFC 8785 canonical JSON (handles TLTV types: strings, ints, bools, arrays, objects)
function cjson(v) {
  if (typeof v === 'string') return JSON.stringify(v)
  if (typeof v !== 'object' || v === null) return String(v)
  if (Array.isArray(v)) return '[' + v.map(cjson).join(',') + ']'
  return '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ':' + cjson(v[k])).join(',') + '}'
}

// Ed25519 signature verification — no private key needed
const SPKI = Buffer.from('302a300506032b6570032100', 'hex')
function verifyDoc(doc) {
  try {
    const id = b58decode(doc.id)
    if (id.length < 34 || id[0] !== 0x14 || id[1] !== 0x33) return false
    const pub = createPublicKey({ key: Buffer.concat([SPKI, id.subarray(2)]), format: 'der', type: 'spki' })
    const body = {}
    for (const k of Object.keys(doc)) if (k !== 'signature') body[k] = doc[k]
    return verify(null, Buffer.from(cjson(body)), pub, b58decode(doc.signature))
  } catch { return false }
}

// Relay state: channelId -> { meta, manifest, segs: Map<name, Buffer> }
const channels = new Map()

async function syncMeta() {
  try {
    const info = await (await fetch(`${UP}/.well-known/tltv`)).json()
    const seen = new Set()
    for (const ch of [...(info.channels || []), ...(info.relaying || [])]) {
      try {
        const meta = await (await fetch(`${UP}/tltv/v1/channels/${ch.id}`)).json()
        if (!verifyDoc(meta)) continue
        if (meta.access === 'token' || meta.on_demand) continue
        seen.add(ch.id)
        const cur = channels.get(ch.id)
        if (!cur) {
          channels.set(ch.id, { meta, manifest: null, segs: new Map() })
          console.log(`  + ${meta.name} (${ch.id})`)
        } else if (meta.seq > cur.meta.seq) {
          if (meta.status === 'retired') { channels.delete(ch.id); continue }
          cur.meta = meta
        }
      } catch {}
    }
    for (const id of channels.keys()) if (!seen.has(id)) channels.delete(id)
  } catch (e) { console.error(`  sync: ${e.message}`) }
}

async function syncHLS() {
  for (const [id, ch] of channels) {
    try {
      ch.manifest = Buffer.from(await (await fetch(`${UP}/tltv/v1/channels/${id}/stream.m3u8`)).arrayBuffer())
      const names = new Set()
      for (const line of ch.manifest.toString().split('\n')) {
        const name = line.trim()
        if (name && !name.startsWith('#')) {
          names.add(name)
          if (!ch.segs.has(name))
            try { ch.segs.set(name, Buffer.from(await (await fetch(`${UP}/tltv/v1/channels/${id}/${name}`)).arrayBuffer())) } catch {}
        }
      }
      for (const k of ch.segs.keys()) if (!names.has(k)) ch.segs.delete(k)
    } catch {}
  }
}

// HTTP server
function json(res, data, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(data))
}

createServer((req, res) => {
  res.setHeader('Access-Control-Allow-Origin', '*')
  res.setHeader('Access-Control-Allow-Methods', 'GET, OPTIONS')
  if (req.method === 'OPTIONS') { res.writeHead(204); return res.end() }
  if (req.method !== 'GET') return json(res, { error: 'invalid_request' }, 400)

  const path = new URL(req.url, `http://${req.headers.host}`).pathname

  if (path === '/.well-known/tltv')
    return json(res, { protocol: 'tltv', versions: [1], channels: [],
      relaying: [...channels.values()].map(c => ({ id: c.meta.id, name: c.meta.name })) })
  if (path === '/tltv/v1/peers') return json(res, { peers: [] })

  const m = path.match(/^\/tltv\/v1\/channels\/([^/]+)(\/.*)?$/)
  if (!m) return json(res, { error: 'invalid_request' }, 400)
  const ch = channels.get(m[1])
  if (!ch) return json(res, { error: 'channel_not_found' }, 404)

  if (!m[2]) return json(res, ch.meta)
  if (m[2] === '/stream.m3u8') {
    if (!ch.manifest) return json(res, { error: 'stream_unavailable' }, 503)
    res.writeHead(200, { 'Content-Type': 'application/vnd.apple.mpegurl', 'Cache-Control': 'max-age=1, no-cache' })
    return res.end(ch.manifest)
  }
  const seg = m[2].match(/^\/(.+\.ts)$/)
  if (seg && ch.segs.has(seg[1])) {
    res.writeHead(200, { 'Content-Type': 'video/mp2t', 'Cache-Control': 'max-age=3600' })
    return res.end(ch.segs.get(seg[1]))
  }
  json(res, { error: 'stream_unavailable' }, 503)
}).listen(PORT, async () => {
  console.log(`TLTV Relay  upstream=${UPSTREAM}`)
  console.log(`Listen:  :${PORT}`)
  console.log(`\nVerify:  tltv node localhost:${PORT} --local`)
  await syncMeta()
  setInterval(syncMeta, 60_000)
  setInterval(syncHLS, 2_000)
})

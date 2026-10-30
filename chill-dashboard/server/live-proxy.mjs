import http from 'node:http'
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import WebSocket, { WebSocketServer } from 'ws'

const FRAME = 1024 * 1024
const QUEUE = 2 * FRAME

export function httpsOrigin(raw) {
  const url = new URL(raw)
  if (url.protocol !== 'https:' || !url.hostname || url.username || url.password || url.pathname !== '/' || url.search || url.hash || raw.includes('?') || raw.includes('#')) throw new Error('Invalid HTTPS origin')
  return url
}

export function validResume(value) {
  return value && value.type === 'resume' && Object.keys(value).every(key => ['type', 'from', 'at'].includes(key)) && Number.isSafeInteger(value.from) && value.from >= 0 && (value.from === 0 ? value.at === undefined : typeof value.at === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(value.at) && Number.isFinite(Date.parse(value.at)))
}

// One in-flight write plus ws's bounded socket buffer; no second application queue.
export function boundedSend(socket, data, fail, state) {
  const bytes = Buffer.byteLength(data)
  if (bytes > FRAME) return fail(1009)
  if (socket.readyState !== WebSocket.OPEN || socket.bufferedAmount + bytes > QUEUE || state.count >= 602) return fail(1013)
  state.count++
  const timer = setTimeout(() => fail(1013), 5000)
  socket.send(data, { binary: false }, error => {
    clearTimeout(timer)
    state.timers.delete(timer)
    state.count--
    if (error) fail(1013)
  })
  state.timers.add(timer)
  timer.unref()
}

export function createFront({ baseURL, publicOrigin, nextPort = 3001, ca } = {}) {
  const upstreamURL = httpsOrigin(baseURL)
  upstreamURL.pathname = '/api/v1/live'
  upstreamURL.protocol = 'wss:'
  const expectedOrigin = publicOrigin ? httpsOrigin(publicOrigin).origin : null
  const wss = new WebSocketServer({ noServer: true, maxPayload: FRAME, perMessageDeflate: false })
  const connections = new Set()
  const server = http.createServer((req, res) => {
    const upstream = http.request({ hostname: '127.0.0.1', port: nextPort, method: req.method, path: req.url, headers: req.headers }, response => {
      res.writeHead(response.statusCode, response.headers)
      response.pipe(res)
    })
    upstream.on('error', () => { if (!res.headersSent) res.writeHead(502); res.end() })
    req.on('aborted', () => upstream.destroy())
    res.on('close', () => upstream.destroy())
    req.pipe(upstream)
  })
  server.on('upgrade', (req, socket, head) => {
    socket.on('error', () => socket.destroy())
    let origin
    try {
      const host = req.headers.host
      if (!host || /[\s/@\\?#]/.test(host)) throw new Error()
      origin = expectedOrigin ?? httpsOrigin(`https://${host}`).origin
    } catch { socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n'); return }
    if (req.method !== 'GET' || req.url !== '/api/live' || req.headers.origin !== origin || req.headers['sec-websocket-protocol'] !== undefined) {
      socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n'); return
    }
    wss.handleUpgrade(req, socket, head, browser => {
      let upstream, authenticated = false, earlyResume, resumed = false, ended = false
      const writes = { count: 0, timers: new Set() }
      const fail = (code = 1013) => {
        if (ended) return
        ended = true
        clearTimeout(authTimer)
        for (const timer of writes.timers) clearTimeout(timer)
        connections.delete(fail)
        for (const ws of [browser, upstream]) {
          if (!ws) continue
          if (ws.readyState === WebSocket.CONNECTING) ws.terminate()
          else if (ws.readyState === WebSocket.OPEN) {
            ws.close(code)
            const timer = setTimeout(() => ws.terminate(), 5000)
            timer.unref()
            ws.once('close', () => clearTimeout(timer))
          }
        }
      }
      connections.add(fail)
      const authTimer = setTimeout(() => fail(4401), 5000)
      browser.on('error', () => fail(authenticated ? 1002 : 4401))
      browser.on('close', () => fail(1000))
      browser.on('message', (data, binary) => {
        if (data.length > 16384) { fail(authenticated ? 1002 : 4401); return }
        let message
        try { if (binary) throw new Error(); message = JSON.parse(data.toString()) } catch { fail(authenticated ? 1002 : 4401); return }
        if (!authenticated) {
          if (!message || message.type !== 'auth' || Object.keys(message).some(key => !['type', 'token'].includes(key)) || typeof message.token !== 'string' || `Bearer ${message.token}`.length > 16384 || !/^[A-Za-z0-9._~+\/-]+=*$/.test(message.token)) { fail(4401); return }
          authenticated = true
          clearTimeout(authTimer)
          upstream = new WebSocket(upstreamURL, { headers: { Authorization: `Bearer ${message.token}` }, ca, rejectUnauthorized: true, followRedirects: false, handshakeTimeout: 10000, maxPayload: FRAME, perMessageDeflate: false })
          upstream.on('unexpected-response', (_request, response) => {
            response.destroy()
            fail(response.statusCode === 401 ? 4401 : response.statusCode === 403 ? 4403 : 1013)
          })
          upstream.on('error', () => fail(1013))
          upstream.on('close', code => fail([1000, 1002, 1009, 1013, 4401, 4403].includes(code) ? code : 1013))
          upstream.on('open', () => { if (earlyResume) boundedSend(upstream, earlyResume, fail, writes) })
          upstream.on('message', (payload, isBinary) => {
            if (isBinary) { fail(1002); return }
            boundedSend(browser, payload, fail, writes)
          })
          return
        }
        if (!validResume(message) || (resumed && upstream.readyState !== WebSocket.OPEN)) { fail(1002); return }
        resumed = true
        if (upstream.readyState === WebSocket.OPEN) boundedSend(upstream, data, fail, writes)
        else earlyResume = data
      })
    })
  })
  server.on('close', () => { for (const close of connections) close(1001); wss.close() })
  server.shutdown = () => { for (const close of connections) close(1001); server.close() }
  return server
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const server = createFront({ baseURL: process.env.CHILL_BASE_URL, publicOrigin: process.env.CHILL_PUBLIC_ORIGIN })
  const child = spawn(process.execPath, ['server.js'], { stdio: 'inherit', env: { ...process.env, HOSTNAME: '127.0.0.1', PORT: '3001' } })
  let stopping = false
  const stop = code => {
    if (stopping) return
    stopping = true
    server.shutdown()
    child.kill('SIGTERM')
    const timer = setTimeout(() => { child.kill('SIGKILL'); process.exit(code) }, 5000)
    timer.unref()
    process.exitCode = code
  }
  child.on('error', () => stop(1))
  child.on('exit', code => stop(stopping ? 0 : code || 1))
  server.on('error', () => stop(1))
  process.on('SIGTERM', () => stop(0))
  process.on('SIGINT', () => stop(0))
  server.listen(Number(process.env.PORT || 3000), process.env.HOSTNAME || '0.0.0.0')
}

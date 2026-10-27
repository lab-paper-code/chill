import test from 'node:test'
import assert from 'node:assert/strict'
import https from 'node:https'
import http from 'node:http'
import { once } from 'node:events'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import WebSocket, { WebSocketServer } from 'ws'
import { createFront, boundedSend, validResume, httpsOrigin } from './live-proxy.mjs'

const listen = async server => { server.listen(0, '127.0.0.1'); await once(server, 'listening'); return server.address().port }
const open = async (url, options) => { const socket = new WebSocket(url, options); await once(socket, 'open'); return socket }
const closeCode = socket => new Promise(resolve => socket.once('close', resolve))

test('origin config, resume validation and bounded queue', () => {
  for (const origin of ['http://localhost', 'https://user:secret@localhost', 'https://localhost/path', 'https://localhost?']) assert.throws(() => httpsOrigin(origin))
  assert.ok(validResume({ type: 'resume', from: 0 }))
  assert.ok(validResume({ type: 'resume', from: 1, at: '2026-10-25T12:00:00.123456789Z' }))
  for (const value of [{ type: 'resume', from: 1 }, { type: 'resume', from: -1 }, { type: 'resume', from: 0, token: 'secret' }]) assert.ok(!validResume(value))
  const state = { count: 0, timers: new Set() }
  let code
  const socket = { readyState: WebSocket.OPEN, bufferedAmount: 2 * 1024 * 1024, send() { assert.fail('unbounded write') } }
  boundedSend(socket, 'x', value => { code = value }, state)
  assert.equal(code, 1013)
  boundedSend(socket, Buffer.alloc(1024 * 1024 + 1), value => { code = value }, state)
  assert.equal(code, 1009)
  socket.bufferedAmount = 0
  state.count = 602
  boundedSend(socket, 'x', value => { code = value }, state)
  assert.equal(code, 1013)
})

test('local TLS upstream receives only bearer and resume; origin, auth, failures, disconnect and HTTP', async t => {
  const directory = mkdtempSync(join(tmpdir(), 'chill-live-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', join(directory, 'key.pem'), '-out', join(directory, 'cert.pem'), '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost'], { stdio: 'ignore' })
  const cert = readFileSync(join(directory, 'cert.pem'))
  const tls = https.createServer({ key: readFileSync(join(directory, 'key.pem')), cert })
  const wss = new WebSocketServer({ noServer: true })
  let calls = 0
  let received
  tls.on('upgrade', (req, socket, head) => {
    calls++
    assert.equal(req.url, '/api/v1/live')
    assert.equal(req.headers.cookie, undefined)
    assert.equal(req.headers.origin, undefined)
    const token = req.headers.authorization
    if (token !== 'Bearer good-token') {
      const status = token === 'Bearer forbidden' ? 403 : token === 'Bearer unavailable' ? 503 : 401
      socket.end(`HTTP/1.1 ${status} Denied\r\nContent-Length: 0\r\nConnection: close\r\n\r\n`)
      return
    }
    // Delay handshake so resume must survive in the single early slot.
    setTimeout(() => wss.handleUpgrade(req, socket, head, upstream => {
      received = upstream
      upstream.send(JSON.stringify({ type: 'hello', seq: 0 }))
      upstream.on('message', (data, binary) => {
        assert.equal(binary, false)
        assert.deepEqual(JSON.parse(data), { type: 'resume', from: 0 })
        upstream.send(JSON.stringify({ type: 'snapshot', seq: 0 }))
      })
    }), 30)
  })
  const upstreamPort = await listen(tls)
  const next = http.createServer((req, res) => { res.writeHead(201, { 'x-original': 'yes' }); res.end(req.url) })
  const nextPort = await listen(next)
  const front = createFront({ baseURL: `https://127.0.0.1:${upstreamPort}`, ca: cert, publicOrigin: 'https://dashboard.example', nextPort })
  const port = await listen(front)
  t.after(() => { front.shutdown(); for (const socket of wss.clients) socket.terminate(); wss.close(); tls.close(); next.close() })
  const url = `ws://127.0.0.1:${port}/api/live`
  const options = { origin: 'https://dashboard.example' }
  const untrusted = createFront({ baseURL: `https://127.0.0.1:${upstreamPort}`, publicOrigin: 'https://dashboard.example', nextPort })
  const untrustedPort = await listen(untrusted)
  t.after(() => untrusted.shutdown())
  const untrustedClient = await open(`ws://127.0.0.1:${untrustedPort}/api/live`, { origin: 'https://dashboard.example' })
  const untrustedClosed = closeCode(untrustedClient)
  untrustedClient.send(JSON.stringify({ type: 'auth', token: 'good-token' }))
  assert.equal(await untrustedClosed, 1013)
  for (const [suffix, origin, protocol] of [['?token=secret', options.origin], ['', undefined], ['', 'https://evil.example'], ['', options.origin, 'secret']]) {
    const client = protocol ? new WebSocket(url + suffix, protocol, { origin }) : new WebSocket(url + suffix, { origin })
    const status = await new Promise(resolve => { client.on('error', () => {}); client.on('unexpected-response', (_req, res) => { resolve(res.statusCode); res.destroy(); client.terminate() }) })
    assert.equal(status, 403)
  }
  assert.equal(calls, 0)
  for (const message of ['not-json', 'x'.repeat(16385), JSON.stringify({ type: 'auth', token: 'bad token' }), JSON.stringify({ type: 'resume', from: 0 })]) {
    const client = await open(url, options)
    const closed = closeCode(client)
    client.send(message)
    assert.equal(await closed, 4401)
  }
  assert.equal(calls, 0)
  for (const [token, expected] of [['invalid', 4401], ['forbidden', 4403], ['unavailable', 1013]]) {
    const client = await open(url, options)
    const closed = closeCode(client)
    client.send(JSON.stringify({ type: 'auth', token }))
    assert.equal(await closed, expected)
  }
  const client = await open(url, options)
  const messages = []
  const baseline = new Promise(resolve => client.on('message', data => { messages.push(JSON.parse(data)); if (messages.length === 2) resolve() }))
  client.send(JSON.stringify({ type: 'auth', token: 'good-token' }))
  client.send(JSON.stringify({ type: 'resume', from: 0 }))
  await baseline
  assert.deepEqual(messages.map(message => message.type), ['hello', 'snapshot'])
  t.diagnostic(`local WSS smoke received: ${messages.map(message => JSON.stringify(message)).join(' -> ')}`)
  const upstreamClosed = closeCode(received)
  client.close()
  await upstreamClosed
  const disconnected = await open(url, options)
  const disconnectedBaseline = once(disconnected, 'message')
  disconnected.send(JSON.stringify({ type: 'auth', token: 'good-token' }))
  await disconnectedBaseline
  const disconnectedClosed = closeCode(disconnected)
  received.close(1013)
  assert.equal(await disconnectedClosed, 1013)
  const duplicate = await open(url, options)
  const duplicateClosed = closeCode(duplicate)
  duplicate.send(JSON.stringify({ type: 'auth', token: 'good-token' }))
  duplicate.send(JSON.stringify({ type: 'auth', token: 'good-token' }))
  assert.equal(await duplicateClosed, 1002)
  const response = await fetch(`http://127.0.0.1:${port}/ordinary?query=1`)
  const silent = await open(url, options)
  assert.equal(await closeCode(silent), 4401)
  const binary = await open(url, options)
  const binaryClosed = closeCode(binary)
  binary.send(Buffer.from('auth'))
  assert.equal(await binaryClosed, 4401)
  assert.equal(response.status, 201)
  assert.equal(response.headers.get('x-original'), 'yes')
  assert.equal(await response.text(), '/ordinary?query=1')
})

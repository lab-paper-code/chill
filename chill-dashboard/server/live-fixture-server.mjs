// Local-only synthetic acceptance harness. Never imported by the application or image entrypoint.
import https from 'node:https'
import { once } from 'node:events'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { WebSocketServer, WebSocket } from 'ws'
import { createFront } from './live-proxy.mjs'
import { fixtureMessages } from '../lib/live-fixture.ts'
import { initialLiveState, parseLiveMessage, applyLiveMessage, liveActionKey } from '../lib/live.ts'
import { parseSchedulerJSON, stringifySchedulerJSON } from '../lib/scheduler.ts'

const capacityFixture = readFileSync(new URL('../lib/fixtures/scheduler-feedback-capacity.json', import.meta.url), 'utf8')
const rateEvidence = parseSchedulerJSON(capacityFixture).tests
const capacityDiagnostic = {periodNanos:1000000000,routeNanos:0,workloads:[{uid:'service-uid',rate:40,queued:3,routingRate:50,retainedRoutingRate:25,weights:{'instance-failed':2},tests:rateEvidence,keepTests:[rateEvidence[2]]}],plans:[]}

const directory = mkdtempSync(join(tmpdir(), 'chill-s2-tls-'))
execFileSync('openssl', ['req','-x509','-newkey','rsa:2048','-nodes','-keyout',join(directory,'key.pem'),'-out',join(directory,'cert.pem'),'-days','1','-subj','/CN=localhost','-addext','subjectAltName=IP:127.0.0.1,DNS:localhost'], {stdio:'ignore'})
const cert = readFileSync(join(directory,'cert.pem')), key = readFileSync(join(directory,'key.pem'))
const port = Number(process.env.CHILL_FIXTURE_PORT || 3445), upstreamPort = port + 1, nextPort = Number(process.env.CHILL_FIXTURE_NEXT_PORT || 3131)
const origin = `https://localhost:${port}`, token = 's2-local-token'
let state = initialLiveState(), seq = 0, scene = 'expand', resyncs = 0, authCount = 0, resumes = [], lastAt = new Date().toISOString()
let measurementMode = 'accept'
const submissions = [], requests = []
let frames = fixtureMessages(scene, lastAt)
for (const frame of frames) { const parsed = parseLiveMessage(frame); if (!parsed) throw new Error('Invalid synthetic frame'); state = applyLiveMessage(state, parsed) }
seq = state.checkpoint.seq
lastAt = state.checkpoint.at
const decisionTimes = new Set([...state.events.flatMap(event=>event.decision?[event.decision.at]:[]),state.snapshot.decision.at])
const wss = new WebSocketServer({noServer:true})
const send = (socket, frame) => { if(socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(frame)) }
function publish(payload) {
  const frame = {...payload,seq:++seq,at:new Date().toISOString()}
  const parsed = parseLiveMessage(frame)
  if (!parsed) throw new Error('Invalid synthetic publication')
  state = applyLiveMessage(state, parsed)
  if(parsed.decision) {
    decisionTimes.add(parsed.decision.at)
    if(decisionTimes.size>1000)decisionTimes.delete(decisionTimes.values().next().value)
  }
  lastAt = frame.at
  for (const socket of wss.clients) if(socket.resumed) send(socket,frame)
}
function changeScene(next) {
  scene = next
  frames = fixtureMessages(scene)
  let projected = initialLiveState()
  for(const frame of frames) projected = applyLiveMessage(projected,parseLiveMessage(frame))
  const remainingActions = new Set(projected.snapshot.actions.map(liveActionKey))
  publish({type:'delta',...projected.snapshot,removed:{
    instances:state.snapshot.instances.filter(row=>!projected.snapshot.instances.some(next=>next.id===row.id)).map(row=>row.id),
    actions:state.snapshot.actions.map(liveActionKey).filter(id=>!remainingActions.has(id)),
  }})
  for(const event of projected.events) publish(event)
  const metric = projected.metrics.at(-1)
  if(metric) publish(metric)
}
const upstream = https.createServer({key,cert}, async (req,res) => {
  if(req.headers.authorization !== `Bearer ${token}`){res.writeHead(401);res.end();return}
  const url = new URL(req.url,'https://localhost')
  if(url.pathname === '/api/v1/namespaces/lab/batch-energy-optimizations' && req.method === 'GET') {
    res.writeHead(200,{'content-type':'application/json'});res.end(JSON.stringify({items:requests}));return
  }
  if(req.method === 'POST' && /^\/api\/v1\/namespaces\/lab\/batch-energy-optimizations(?:\/preflight)?$/.test(url.pathname)) {
    if(url.searchParams.has('dryRun') || url.pathname.endsWith('/preflight')) {
      res.writeHead(503,{'content-type':'application/json'});res.end(JSON.stringify({code:'FixtureDiagnosticUnavailable',message:'Optional check unavailable'}));return
    }
    req.setEncoding('utf8')
    let raw = ''
    for await (const chunk of req) {
      raw += chunk
      if(Buffer.byteLength(raw)>1<<20){res.writeHead(413);res.end();return}
    }
    let body
    try { body=JSON.parse(raw) } catch {res.writeHead(400);res.end();return}
    submissions.push(body)
    if(measurementMode === 'reject') {
      res.writeHead(422,{'content-type':'application/json'});res.end(JSON.stringify({code:'FixtureRejected',message:'Final server rejected these conditions'}));return
    }
    const task = {...body,metadata:{...body.metadata,uid:`fixture-request-${submissions.length}`,resourceVersion:'1',creationTimestamp:new Date().toISOString()}}
    requests.push(task)
    if(measurementMode === 'lost'){res.destroy();return}
    if(measurementMode === 'slow')await new Promise(resolve=>setTimeout(resolve,5000))
    res.writeHead(201,{'content-type':'application/json'});res.end(JSON.stringify({request:'Accepted',task}));return
  }
  if(url.pathname === '/api/v1/scheduler/capacity') {
    res.writeHead(200,{'content-type':'application/json','cache-control':'no-store'})
    res.end(scene === 'empty' ? JSON.stringify({tests:[]}) : capacityFixture);return
  }
  if(url.pathname === '/api/v1/scheduler/state') {
    res.writeHead(200,{'content-type':'application/json','cache-control':'no-store'})
    res.end(stringifySchedulerJSON({decision:{at:lastAt,capacity:scene === 'empty' ? {...capacityDiagnostic,workloads:[]} : capacityDiagnostic}}));return
  }
  if(url.pathname === '/api/v1/observations/scheduler') {
    const from = url.searchParams.get('from')
    const at = [...decisionTimes].find(value=>value.replace(/(?:\.(\d+))?Z$/,(_match,fraction='')=>`.${fraction.padEnd(9,'0')}Z`)===from)
    const found = at !== undefined
    res.writeHead(200,{'content-type':'application/json','cache-control':'no-store'})
    res.end(stringifySchedulerJSON({from:at,to:url.searchParams.get('to'),events:found?[{at,decision:{at,status:'TransitionInProgress',reasons:['검사용 고정 근거'],keep:{id:'fixture-keep',keep:true,actions:null,status:'Feasible',reasons:null,expectedJoules:1000,worstExceedance:0},target:null,capacity:{...capacityDiagnostic,gainUncertaintyJoules:25}}}]:[]}))
    return
  }
  res.writeHead(404);res.end()
})
upstream.on('upgrade',(req,socket,head)=>{
  if(req.url!=='/api/v1/live'||req.headers.authorization!==`Bearer ${token}`){socket.end('HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n');return}
  authCount++
  wss.handleUpgrade(req,socket,head,client=>{
    send(client,{type:'hello',seq,at:new Date().toISOString(),protocol:'chill-live/1',serverAt:new Date().toISOString(),retainFrom:seq+1})
    client.on('message',data=>{
      const message = JSON.parse(data.toString())
      if(message.type!=='resume'){client.close(1002);return}
      resumes.push(message);resumes=resumes.slice(-10)
      // A deliberately zero-retention hub exercises the real out-of-range snapshot path.
      send(client,{type:'snapshot',seq,at:lastAt,...state.snapshot})
      client.resumed=true
    })
  })
})
upstream.listen(upstreamPort,'127.0.0.1');await once(upstream,'listening')
const front = createFront({baseURL:`https://127.0.0.1:${upstreamPort}`,ca:cert,publicOrigin:origin,nextPort})
const browser = https.createServer({key,cert},(req,res)=>{
  const url = new URL(req.url,origin)
  if(url.pathname.startsWith('/__fixture/')) {
    if(req.method==='POST'&&url.pathname==='/__fixture/scene'&&['empty','first','expand','shrink'].includes(url.searchParams.get('name')))changeScene(url.searchParams.get('name'))
    if(req.method==='POST'&&url.pathname==='/__fixture/resync') {
      seq+=1000;resyncs++;lastAt=new Date().toISOString()
      state=applyLiveMessage(state,{type:'snapshot',seq,at:lastAt,...state.snapshot})
      for(const client of wss.clients)client.close(1013)
    }
    if(req.method==='POST'&&url.pathname==='/__fixture/cluster')publish({type:'event',kind:'cluster',cluster:{kind:'issue',at:new Date().toISOString(),event:'issue',source:'observer',reason:'검사용 failure 실시간 장애',previousReason:'검사용 이전 사유'}})
    if(req.method==='POST'&&url.pathname==='/__fixture/additive')publish({type:'delta',extension:{version:2},nodes:state.snapshot.nodes.map(row=>({...row,extension:{driver:'new'}})),workloads:state.snapshot.workloads.map(row=>({...row,extension:{diagnostics:[]},readiness:{...row.readiness,extension:true}}))})
    if(req.method==='POST'&&url.pathname==='/__fixture/measurement'&&['accept','reject','lost','slow'].includes(url.searchParams.get('mode')))measurementMode=url.searchParams.get('mode')
    res.writeHead(200,{'content-type':'application/json','cache-control':'no-store'});res.end(JSON.stringify({scene,authCount,resumes,resyncs,connections:wss.clients.size,seq,measurementMode,submissions,requests}));return
  }
  front.emit('request',req,res)
})
browser.on('upgrade',(req,socket,head)=>front.emit('upgrade',req,socket,head))
browser.listen(port,'127.0.0.1');await once(browser,'listening')
const timer = setInterval(()=>{
  const latest=state.metrics.at(-1)
  if(latest&&wss.clients.size)publish({...latest,bucketAt:new Date(Date.now()-1000).toISOString().replace(/\.\d{3}Z$/,'.000Z')})
},1000)
console.log(`S2 fixture ready ${origin}; upstream=https://127.0.0.1:${upstreamPort}; CA=${join(directory,'cert.pem')}; nextPort=${nextPort}`)
function stop(){clearInterval(timer);for(const client of wss.clients)client.terminate();wss.close();front.shutdown();browser.close();upstream.close();rmSync(directory,{recursive:true,force:true});process.exit(0)}
process.on('SIGTERM',stop);process.on('SIGINT',stop)

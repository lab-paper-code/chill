import assert from "node:assert/strict"
import test from "node:test"
import type { LiveMetrics } from "./live.ts"
import type { OperatingPowerSeries } from "./read-types.ts"
import { mergePowerSeries, powerSeriesPath } from "./power-series.ts"
import * as power from "./power-series.ts"
import { stringifySchedulerJSON } from "./scheduler.ts"

const at=(seconds:number)=>new Date(Date.parse("2026-11-01T00:00:00Z")+seconds*1000).toISOString()
const series={from:at(0),to:at(20),bucketNanos:5000000000,buckets:[0,5,10,15].map((second,index)=>({from:at(second),to:at(second+5),wholePoolMeanWatts:index===1?null:index===0?0:20,activePoolMeanWatts:0,nodes:[{node:{uid:"n",name:"노드"},meanWatts:index===1?null:0}]}))} as OperatingPowerSeries
const live=(second:number,watts:number|null):LiveMetrics=>({type:"metrics",chill_obslog_queue_length:0,chill_obslog_oldest_wait_seconds:0,seq:second,at:at(second+1),bucketAt:at(second),workloads:{},nodes:{n:{watts,observedAt:at(second)}},pool:{wholeWatts:watts,activeWatts:0}})

test("power series paths stay within one hour and 900 UTC-aligned buckets",()=>{
  for(const [seconds,bucket] of [[900,"5s"],[3600,"5s"],[899,"1s"]] as const)assert.equal(new URL(powerSeriesPath(at(.5),at(seconds+.5),bucket)!,"https://local").searchParams.get("bucket"),bucket)
  for(const [seconds,bucket] of [[3601,"5s"],[900,"1s"],[0,"5s"]] as const)assert.equal(powerSeriesPath(at(.5),at(seconds+.5),bucket),null)
})

// The column names and nesting match OperatingPowerSeries.Columns in Go.
function columns(from:string,to:string,count:number) {
  return {
    from,to,bucketNanos:5000000000,format:"columns",
    windows:{
      scope:Array(count).fill("whole-pool-wall"),wholePoolJoules:Array(count).fill(null),wholePoolMeanWatts:Array(count).fill(null),
      activePoolJoules:Array(count).fill(0),activePoolMeanWatts:Array(count).fill(0),complete:Array(count).fill(false),
      readFailures:Array(count).fill(3),maximumSampleGapSeconds:Array(count).fill(.761647874),
      reasons:Array.from({length:count},()=>["no observed samples in bucket"]),gapUntil:Array(count).fill(null),
      coverage:Array.from({length:count},()=>({from,to,sources:[{from,to,stream:"scheduler",source:"local"}],gaps:[{from,to,stream:"scheduler",reason:"StreamUnavailable"}]})),
    },
    nodes:{
      node:Array.from({length:count},()=>[{uid:"missing",name:"노드"}]),meter:Array.from({length:count},()=>["meter"]),
      scope:Array.from({length:count},()=>["node-exclusive-wall"]),joules:Array.from({length:count},()=>[null]),
      meterJoules:Array.from({length:count},()=>[null as number|null]),meterComplete:Array.from({length:count},()=>[false]),
      gapSeconds:Array.from({length:count},()=>[5]),gapFraction:Array.from({length:count},()=>[1]),
      sourceIdentity:Array.from({length:count},()=>[{kind:"edge-metrics",endpoint:"http://meter",metric:"wall_watts"}]),
      meanWatts:Array.from({length:count},()=>[null]),samples:Array.from({length:count},()=>[0]),
      readFailures:Array.from({length:count},()=>[3]),maximumSampleGapSeconds:Array.from({length:count},()=>[.761647874]),
      sourceTimestampAbsent:Array.from({length:count},()=>[true]),observedSamples:Array.from({length:count},()=>[null]),
      firstObservedAt:Array.from({length:count},()=>[null]),lastObservedAt:Array.from({length:count},()=>[null]),
      reasons:Array.from({length:count},()=>[["missing source"]]),
    },
  }
}

test("columns decoder restores real DTO rows losslessly, including nanosecond clipping and null versus empty nodes",()=>{
  const from="2026-11-01T00:00:00.123456789Z",to="2026-11-01T00:00:20.987654321Z"
  const wire=columns(from,to,5)
  wire.windows.wholePoolJoules[1]=0 as never
  wire.windows.wholePoolMeanWatts[1]=0 as never
  wire.windows.complete[1]=true
  wire.windows.reasons[1]=[]
  wire.windows.reasons[2]=null as never
  wire.windows.gapUntil[0]=to as never
  wire.nodes.samples[0][0]=9007199254740993n as never
  wire.nodes.observedSamples[0][0]=0 as never
  wire.nodes.firstObservedAt[0][0]=from as never
  wire.nodes.lastObservedAt[0][0]=to as never
  wire.nodes.meterJoules[1][0]=0
  wire.nodes.meterComplete[1][0]=true
  wire.nodes.gapSeconds[1][0]=0
  wire.nodes.gapFraction[1][0]=0
  for(const column of Object.values(wire.nodes)){column[3]=null as never;column[4]=[]}
  const decoded=power.parsePowerSeriesJSON(stringifySchedulerJSON(wire))
  assert.equal(decoded.from,from)
  assert.equal(decoded.to,to)
  assert.equal(decoded.bucketNanos,5000000000)
  assert.deepEqual(decoded.buckets.map(row=>[row.from,row.to]),[
    [from,"2026-11-01T00:00:05Z"],["2026-11-01T00:00:05Z","2026-11-01T00:00:10Z"],
    ["2026-11-01T00:00:10Z","2026-11-01T00:00:15Z"],["2026-11-01T00:00:15Z","2026-11-01T00:00:20Z"],["2026-11-01T00:00:20Z",to],
  ])
  assert.deepEqual(decoded.buckets[0],{
    from,to:"2026-11-01T00:00:05Z",scope:"whole-pool-wall",wholePoolJoules:null,wholePoolMeanWatts:null,
    activePoolJoules:0,activePoolMeanWatts:0,complete:false,readFailures:3,maximumSampleGapSeconds:.761647874,
    reasons:["no observed samples in bucket"],gapUntil:to,coverage:wire.windows.coverage[0],
    nodes:[{node:{uid:"missing",name:"노드"},meter:"meter",scope:"node-exclusive-wall",joules:null,meanWatts:null,
      meterJoules:null,meterComplete:false,gapSeconds:5,gapFraction:1,sourceIdentity:wire.nodes.sourceIdentity[0][0],
      samples:9007199254740993n,readFailures:3,maximumSampleGapSeconds:.761647874,sourceTimestampAbsent:true,
      observedSamples:0,firstObservedAt:from,lastObservedAt:to,reasons:["missing source"]}],
  })
  assert.equal(decoded.buckets[1].wholePoolMeanWatts,0)
  assert.equal(decoded.buckets[1].complete,true)
  assert.equal(decoded.buckets[1].nodes![0].meterJoules,0)
  assert.equal(decoded.buckets[1].nodes![0].meterComplete,true)
  assert.equal(decoded.buckets[1].nodes![0].gapSeconds,0)
  assert.equal(decoded.buckets[1].nodes![0].gapFraction,0)
  assert.equal("gapUntil" in decoded.buckets[1],false)
  assert.equal("observedSamples" in decoded.buckets[1].nodes![0],false)
  assert.equal(decoded.buckets[2].reasons,null)
  assert.equal(decoded.buckets[3].nodes,null)
  assert.deepEqual(decoded.buckets[4].nodes,[])
})

test("aligned columns and an empty columns result retain their exact intervals",()=>{
  const aligned=power.parsePowerSeriesJSON(JSON.stringify(columns(at(0),at(10),2)))
  assert.deepEqual(aligned.buckets.map(row=>[Date.parse(row.from),Date.parse(row.to)]),[[0,5],[5,10]].map(pair=>pair.map(second=>Date.parse(at(second)))))
  assert.deepEqual(power.parsePowerSeriesJSON(JSON.stringify(columns(at(0),at(10),0))).buckets,[])
})

test("old row responses and unknown malformed shapes report a clear format mismatch, never empty success",()=>{
  for(const response of [series,{},{from:at(0),to:at(10),format:"columns",bucketNanos:5000000000}]){
    assert.throws(()=>power.parsePowerSeriesJSON(JSON.stringify(response)),/형식 불일치/)
  }
})

test("power seed keeps actual five-second intervals, appends live, preserves null breaks and measured zero",()=>{
  const points=mergePowerSeries(series,[live(15,0),live(16,null),live(18,30)],Date.parse(at(19)))
  assert.deepEqual(points.map(row=>[row.from,row.to,row.wholeWatts]),[[0,5,0],[5,10,null],[10,15,20],[15,16,0],[16,17,null],[18,19,30]].map(([from,to,watts])=>[Date.parse(at(from as number)),Date.parse(at(to as number)),watts]))
  assert.equal(points[0].nodes.n,0)
  assert.equal(mergePowerSeries(series,[],Date.parse(at(3600)),3600000).length,4)
  assert.equal(mergePowerSeries(series,[],Date.parse(at(3600))).length,0)
  assert.equal(mergePowerSeries(series,[live(-1000,999)],Date.parse(at(20))).length,4)
  assert.deepEqual(mergePowerSeries(series,[live(0,0)],Date.parse(at(20))).map(row=>row.to),[1,10,15,20].map(second=>Date.parse(at(second))))
})

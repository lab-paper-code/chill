import assert from "node:assert/strict"
import test from "node:test"
import type { LiveMetrics } from "./live.ts"
import type { OperatingPowerSeries } from "./read-types.ts"
import { mergePowerSeries, powerSegments, powerSeriesPath } from "./power-series.ts"

const at=(seconds:number)=>new Date(Date.parse("2026-11-01T00:00:00Z")+seconds*1000).toISOString()
const series={from:at(0),to:at(20),bucketNanos:5000000000,buckets:[0,5,10,15].map((second,index)=>({from:at(second),to:at(second+5),wholePoolMeanWatts:index===1?null:index===0?0:20,activePoolMeanWatts:0,nodes:[{node:{uid:"n",name:"노드"},meanWatts:index===1?null:0}]}))} as OperatingPowerSeries
const live=(second:number,watts:number|null):LiveMetrics=>({type:"metrics",seq:second,at:at(second+1),bucketAt:at(second),workloads:{},nodes:{n:{watts,observedAt:at(second)}},pool:{wholeWatts:watts,activeWatts:0}})

test("power seed keeps actual five-second intervals, appends live, preserves null breaks and measured zero",()=>{
  const points=mergePowerSeries(series,[live(15,0),live(16,null),live(18,30)],Date.parse(at(19)))
  assert.deepEqual(points.map(row=>[row.from,row.to,row.wholeWatts]),[[0,5,0],[5,10,null],[10,15,20],[15,16,0],[16,17,null],[18,19,30]].map(([from,to,watts])=>[Date.parse(at(from as number)),Date.parse(at(to as number)),watts]))
  assert.equal(points[0].nodes.n,0)
  assert.deepEqual(powerSegments(points).map(segment=>segment.map(row=>row.wholeWatts)),[[0],[20,0],[30]])
  assert.equal(mergePowerSeries(series,[],Date.parse(at(3600)),3600000).length,4)
  assert.equal(mergePowerSeries(series,[],Date.parse(at(3600))).length,0)
  assert.equal(mergePowerSeries(series,[live(-1000,999)],Date.parse(at(20))).length,4)
  assert.deepEqual(mergePowerSeries(series,[live(0,0)],Date.parse(at(20))).map(row=>row.to),[1,10,15,20].map(second=>Date.parse(at(second))))
})

test("power series paths stay within one hour and 900 UTC-aligned buckets",()=>{
  for(const [seconds,bucket] of [[900,"5s"],[3600,"5s"],[899,"1s"]] as const)assert.equal(new URL(powerSeriesPath(at(.5),at(seconds+.5),bucket)!,"https://local").searchParams.get("bucket"),bucket)
  for(const [seconds,bucket] of [[3601,"5s"],[900,"1s"],[0,"5s"]] as const)assert.equal(powerSeriesPath(at(.5),at(seconds+.5),bucket),null)
})

import type { LiveMetrics } from "./live"
import type { OperatingPower, OperatingPowerSeries } from "./read-types"
import { parseSchedulerJSON } from "./scheduler.ts"

export const POWER_BUCKETS = {"1s":1000,"5s":5000,"30s":30000,"1m":60000,"5m":300000,"1h":3600000} as const
export type PowerBucket = keyof typeof POWER_BUCKETS
export type PowerPoint = {from:number;to:number;wholeWatts:number|null;activeWatts:number|null;nodes:Record<string,number|null>}

export function powerSeriesPath(from:string,to:string,bucket:PowerBucket="5s") {
  const start=timestampNanos(from),end=timestampNanos(to),width=BigInt(POWER_BUCKETS[bucket])*1000000n
  if(start===null||end===null||end<=start||end-start>3600000000000n||(end-start+start%width+width-1n)/width>900n)return null
  return `v1/observations/power?${new URLSearchParams({from,to,bucket,format:"columns"})}`
}

function timestampNanos(at:string):bigint|null {
  const match=/^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(at)
  if(!match)return null
  const seconds=Date.parse(`${match[1]}${match[3]}`)
  return Number.isFinite(seconds)?BigInt(seconds)*1000000n+BigInt((match[2]??"").padEnd(9,"0")):null
}
function nanosTimestamp(at:bigint):string {
  const seconds=at/1000000000n,fraction=String(at%1000000000n).padStart(9,"0").replace(/0+$/,"")
  return new Date(Number(seconds)*1000).toISOString().replace(".000Z",`${fraction?`.${fraction}`:""}Z`)
}

const windowKeys=["scope","wholePoolJoules","wholePoolMeanWatts","activePoolJoules","activePoolMeanWatts","complete","readFailures","maximumSampleGapSeconds","reasons","gapUntil","coverage"] as const
const nodeKeys=["node","meter","scope","meterJoules","meterComplete","gapSeconds","gapFraction","sourceIdentity","joules","meanWatts","samples","readFailures","maximumSampleGapSeconds","sourceTimestampAbsent","observedSamples","firstObservedAt","lastObservedAt","reasons"] as const
const optionalNodeKeys:Record<string,true>={observedSamples:true,firstObservedAt:true,lastObservedAt:true}
function record(value:unknown):value is Record<string,unknown> {return !!value&&typeof value==="object"&&!Array.isArray(value)}

/** Reverse Go OperatingPowerSeries.Columns without rounding timestamps or measured values. */
export function parsePowerSeriesJSON(text:string):OperatingPowerSeries {
  const wire=parseSchedulerJSON(text)
  if(!record(wire)||wire.format!=="columns"||typeof wire.from!=="string"||typeof wire.to!=="string"||
    (typeof wire.bucketNanos!=="number"&&typeof wire.bucketNanos!=="bigint")||!record(wire.windows)||!record(wire.nodes))throw new Error("전력 형식 불일치: columns 응답이 필요해요.")
  const {from,to,bucketNanos,windows,nodes}=wire,start=timestampNanos(from),end=timestampNanos(to),width=BigInt(bucketNanos)
  if(start===null||end===null||end<=start||width<=0n||!Array.isArray(windows.scope))throw new Error("전력 구간이 올바르지 않아요.")
  const count=windows.scope.length
  const windowColumns=windowKeys.map(key=> {
    const values=windows[key]
    if(!Array.isArray(values)||values.length!==count)throw new Error("전력 구간 열 길이가 일치하지 않아요.")
    return {key,values}
  })
  const nodeColumns=nodeKeys.map(key=>{
    const values=nodes[key]
    if(!Array.isArray(values)||values.length!==count)throw new Error("전력 장치 열 길이가 일치하지 않아요.")
    return {key,values}
  })
  const aligned=start-start%width
  const buckets=Array.from({length:count},(_,index)=>{
    const left=aligned+BigInt(index)*width,right=left+width,identities=nodeColumns[0].values[index]
    if(identities!==null&&!Array.isArray(identities))throw new Error("전력 장치 열이 올바르지 않아요.")
    const length=identities?.length??0
    for(const column of nodeColumns) {
      const values=column.values[index]
      if(identities===null?values!==null:!Array.isArray(values)||values.length!==length)throw new Error("전력 장치 열 길이가 일치하지 않아요.")
    }
    const restoredNodes=identities===null?null:Array.from({length},(_,nodeIndex)=>Object.fromEntries(nodeColumns.flatMap(({key,values})=>{
      const value=values[index][nodeIndex]
      return value===null&&optionalNodeKeys[key]?[]:[[key,value]]
    })))
    // Column lengths and required fields were checked above; reconstruction retains the DTO field types.
    const row=Object.fromEntries(windowColumns.flatMap(({key,values})=>key==="gapUntil"&&values[index]===null?[]:[[key,values[index]]])) as Omit<OperatingPower,"from"|"to"|"nodes">
    return {...row,from:left<=start?from:nanosTimestamp(left),to:right>=end?to:nanosTimestamp(right),nodes:restoredNodes} as OperatingPower
  })
  return {from,to,bucketNanos,buckets}
}

/** Historical intervals where no live interval covers them; one that runs into a live interval ends where that one starts, so the line meets live data without a gap. */
export function mergePowerSeries(seed:OperatingPowerSeries|undefined,metrics:LiveMetrics[],end:number,retentionMS=900000):PowerPoint[] {
  const live:PowerPoint[]=metrics.map(row=>({from:Date.parse(row.bucketAt),to:Date.parse(row.bucketAt)+1000,wholeWatts:row.pool.wholeWatts,activeWatts:row.pool.activeWatts,nodes:Object.fromEntries(Object.entries(row.nodes).map(([uid,node])=>[uid,node.watts]))})).filter(row=>row.to>end-retentionMS&&row.to<=end).sort((a,b)=>a.from-b.from)
  let liveIndex=0
  const history:PowerPoint[]=(seed?.buckets??[]).map(row=>({from:Date.parse(row.from),to:Date.parse(row.to),wholeWatts:row.wholePoolMeanWatts,activeWatts:row.activePoolMeanWatts,nodes:Object.fromEntries((row.nodes??[]).map(node=>[node.node.uid,node.meanWatts]))})).flatMap(row=>{
    if(row.to<=end-retentionMS||row.to>end)return []
    while(liveIndex<live.length&&live[liveIndex].to<=row.from)liveIndex++
    const next=live[liveIndex]
    if(!next||next.from>=row.to)return [row]
    return next.from>row.from?[{...row,to:next.from}]:[]
  })
  return [...history,...live].sort((a,b)=>a.from-b.from).slice(-900)
}

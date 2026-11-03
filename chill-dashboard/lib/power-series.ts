import type { LiveMetrics } from "./live"
import type { OperatingPowerSeries } from "./read-types"

export const POWER_BUCKETS = {"1s":1000,"5s":5000,"30s":30000,"1m":60000,"5m":300000,"1h":3600000} as const
export type PowerBucket = keyof typeof POWER_BUCKETS
export type PowerPoint = {from:number;to:number;wholeWatts:number|null;activeWatts:number|null;nodes:Record<string,number|null>}

export function powerSeriesPath(from:string,to:string,bucket:PowerBucket="5s") {
  const start=Date.parse(from),end=Date.parse(to),width=POWER_BUCKETS[bucket]
  if(!Number.isFinite(start)||!Number.isFinite(end)||end<=start||end-start>3600000||Math.ceil((end-Math.floor(start/width)*width)/width)>900)return null
  return `v1/observations/power?${new URLSearchParams({from,to,bucket})}`
}

/** Keep true historical intervals only where they do not overlap a live interval. */
export function mergePowerSeries(seed:OperatingPowerSeries|undefined,metrics:LiveMetrics[],end:number,retentionMS=900000):PowerPoint[] {
  const live:PowerPoint[]=metrics.map(row=>({from:Date.parse(row.bucketAt),to:Date.parse(row.bucketAt)+1000,wholeWatts:row.pool.wholeWatts,activeWatts:row.pool.activeWatts,nodes:Object.fromEntries(Object.entries(row.nodes).map(([uid,node])=>[uid,node.watts]))})).filter(row=>row.to>end-retentionMS&&row.to<=end).sort((a,b)=>a.from-b.from)
  let liveIndex=0
  const history:PowerPoint[]=(seed?.buckets??[]).map(row=>({from:Date.parse(row.from),to:Date.parse(row.to),wholeWatts:row.wholePoolMeanWatts,activeWatts:row.activePoolMeanWatts,nodes:Object.fromEntries((row.nodes??[]).map(node=>[node.node.uid,node.meanWatts]))})).filter(row=>{
    if(row.to<=end-retentionMS||row.to>end)return false
    while(liveIndex<live.length&&live[liveIndex].to<=row.from)liveIndex++
    return liveIndex===live.length||live[liveIndex].from>=row.to
  })
  return [...history,...live].sort((a,b)=>a.from-b.from).slice(-900)
}

/** Null intervals and missing time both break the line. Measured zero is not missing. */
export function powerSegments(points:PowerPoint[],nodeUID?:string):PowerPoint[][] {
  const segments:PowerPoint[][]=[]
  let current:PowerPoint[]=[]
  for(const point of points){
    const watts=nodeUID?point.nodes[nodeUID]:point.wholeWatts
    if(watts==null){current=[];continue}
    if(!current.length||current.at(-1)!.to!==point.from){current=[];segments.push(current)}
    current.push(point)
  }
  return segments
}

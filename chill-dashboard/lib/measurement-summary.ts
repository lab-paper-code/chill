import { parseSchedulerJSON } from "./scheduler.ts"

// The dictionary is a response-local DAG: entries only refer to earlier entries.
// Keep shared values shared instead of rebuilding source arrays for every check.
export function restoreMeasurementSummary(value:unknown):unknown {
  if(!value||typeof value!=="object"||Array.isArray(value)||!("readProjection" in value))return value
  const projection=value.readProjection as {version?:unknown;values?:unknown[];shapes?:string[][]}|null
  if(projection?.version!=="measurement-summary.v1"||!Array.isArray(projection.values)||!Array.isArray(projection.shapes))throw new Error("측정 요약 형식 미지원")
  const shapes=projection.shapes
  const restored:unknown[]=[]
  function restore(item:unknown):unknown {
    if(typeof item==="string"&&/^~\d+$/.test(item)) {
      const index=Number(item.slice(1))
      if(!Number.isSafeInteger(index)||index>=restored.length)throw new Error("측정 근거 참조 미확인")
      return restored[index]
    }
    if(Array.isArray(item))return item.map(restore)
    if(!item||typeof item!=="object")return item
    if("$o" in item&&Object.keys(item).length===1) {
      const fields=item.$o
      if(!Array.isArray(fields)||typeof fields[0]!=="number"||!Number.isInteger(fields[0]))throw new Error("측정 필드 참조 미확인")
      const keys=shapes[fields[0]]
      if(!Array.isArray(keys)||fields.length!==keys.length+1||keys.some(key=>typeof key!=="string"))throw new Error("측정 필드 참조 미확인")
      return Object.fromEntries(keys.map((key,index)=>[key,restore(fields[index+1])]))
    }
    return Object.fromEntries(Object.entries(item).map(([key,entry])=>[key,restore(entry)]))
  }
  for(const entry of projection.values)restored.push(typeof entry==="string"?entry:restore(entry))
  const {readProjection,...body}=value
  void readProjection
  return restore(body)
}

export function parseMeasurementJSON(text:string):unknown {
  return restoreMeasurementSummary(parseSchedulerJSON(text))
}

export function timestampNanos(value:string):bigint|null {
  const match=/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(value)
  if(!match)return null
  const [,year,month,day,hour,minute,second,fraction="",zone]=match
  const date=new Date(`${year}-${month}-${day}T${hour}:${minute}:${second}Z`)
  if(!Number.isFinite(date.getTime())||date.toISOString().slice(0,19)!==`${year}-${month}-${day}T${hour}:${minute}:${second}`)return null
  let offset=0
  if(zone!=="Z") {
    const h=Number(zone.slice(1,3)),m=Number(zone.slice(4,6))
    if(h>23||m>59)return null
    offset=(h*60+m)*(zone[0]==="+"?1:-1)
  }
  return BigInt(date.getTime()-offset*60000)*1000000n+BigInt(fraction.padEnd(9,"0"))
}

export function localDateTime(value:string):string {
  const date=new Date(value)
  return Number.isFinite(date.getTime())?new Date(date.getTime()-date.getTimezoneOffset()*60000).toISOString().slice(0,16):""
}

export function utcDateTime(value:string):string {
  const date=new Date(value)
  return Number.isFinite(date.getTime())?date.toISOString():""
}

// gapUntil: ChillServer keeps local observation for a bounded period; earlier parts of a requested interval are a gap.
export const gapNotice=(gapUntil:string)=>`관측 공백: ${localDateTime(gapUntil)} 이전은 로컬 보존 범위 밖이에요`

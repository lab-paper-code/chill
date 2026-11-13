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

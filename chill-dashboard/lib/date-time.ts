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

export function recordKind(params:Pick<URLSearchParams,"get">):string {
  return params.get("kind")==="requests"?"requests":params.get("kind")==="power"?"power":"judgments"
}

// Cursors and exact-record references belong to one API and one submitted window.
export function recordKindParams(params:URLSearchParams,kind:string):URLSearchParams {
  const next=new URLSearchParams(params)
  for(const key of [...next.keys()])if(!["from","to","uid","limit","namespace"].includes(key))next.delete(key)
  next.set("kind",kind)
  return next
}

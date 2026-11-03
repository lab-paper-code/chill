import { applyRecordedLivePage, compareAt, parseRecordedLivePage, type LiveState } from "./live.ts"

/** Five fixed-prefix pages, never a full archive scan or a live checkpoint source. */
export async function seedRecentLiveEvents(request:(path:string,signal:AbortSignal)=>Promise<unknown>,update:(change:(state:LiveState)=>LiveState)=>void,signal:AbortSignal,now=Date.now()):Promise<void> {
  const from=new Date(now-15*60_000).toISOString(),to=new Date(now).toISOString()
  let cursor:string|undefined
  for(let pageIndex=0;pageIndex<5;pageIndex++) {
    signal.throwIfAborted()
    const query=new URLSearchParams({from,to,limit:"100"})
    if(cursor!==undefined)query.set("cursor",cursor)
    const response=await request(`v1/observations/events?${query}`,signal)
    signal.throwIfAborted()
    const page=parseRecordedLivePage(response)
    if(!page || compareAt(page.from,from)!==0 || compareAt(page.to,to)!==0)throw new Error("Invalid recent event archive")
    update(state=>signal.aborted || state.status==="unauthorized" || state.status==="forbidden" ? state : applyRecordedLivePage(state,page,pageIndex===0))
    cursor=page.nextCursor
    if(cursor===undefined)return
  }
}

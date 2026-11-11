"use client"

import * as React from "react"
import { skipToken, useQuery, useQueryClient } from "@tanstack/react-query"
import { usePathname, useSearchParams } from "next/navigation"
import { useSession } from "@/lib/client"
import { ApiError, errorDetails, relay, warmingRetry, warmingDelay, isCoverageWarming } from "@/lib/client-transport"
import { applyLiveMessage, connectLive, initialLiveState, parseLiveMessage, type LiveConnection, type LiveState } from "@/lib/live"
import { fixtureMessages } from "@/lib/live-fixture"
import { seedRecentLiveEvents } from "@/lib/live-history"
import { SourceJSON } from "@/components/read-region"

type LiveContextValue = {state:LiveState;status:string;reconnect:()=>void}
const LiveContext=React.createContext<LiveContextValue|null>(null)

export function LiveProvider({children}:{children:React.ReactNode}) {
  const {token,epoch,config,signals}=useSession()
  const queryClient=useQueryClient()
  const search=useSearchParams()
  const operating=usePathname()==="/"
  const requested=search.get("fixture")
  const scene=config.liveFixture && requested && ["empty","first","expand","shrink"].includes(requested) ? requested : null
  const liveKey=React.useMemo(()=>["chill-live",epoch,scene] as const,[epoch,scene])
  const {data}=useQuery<LiveState>({queryKey:liveKey,queryFn:skipToken,enabled:false,initialData:initialLiveState,staleTime:Infinity,gcTime:Infinity})
  // skipToken's overload permits undefined; initialData always seeds this cache-only query.
  const state=data!
  const control=React.useRef<LiveConnection|null>(null)
  const reconnect=React.useCallback(()=>control.current?.reconnect(),[])
  const [archiveAttempt,setArchiveAttempt]=React.useState(0)

  React.useEffect(()=>{
    if (!token) return
    const abort=new AbortController()
    signals.add(abort)
    const update=(change:(state:LiveState)=>LiveState)=>{if(abort.signal.aborted)return;queryClient.setQueryData<LiveState>(liveKey,state=>change(state ?? initialLiveState()));if(queryClient.getQueryData<LiveState>(liveKey)?.status==="unauthorized")window.dispatchEvent(new Event("chill-unauthorized"))}
    const read=()=>queryClient.getQueryData<LiveState>(liveKey) ?? initialLiveState()
    let connection:LiveConnection
    if (scene) {
      const replay=()=>{
        let next=initialLiveState()
        for (const payload of fixtureMessages(scene)) {
          const message=parseLiveMessage(payload)
          if (!message) { next={...initialLiveState(),status:"protocol-error"};break }
          next=applyLiveMessage(next,message)
        }
        queryClient.setQueryData(liveKey,{...next,status:next.status === "protocol-error" ? next.status : "fixture"})
      }
      replay()
      connection={reconnect:replay,close:()=>{}}
    } else connection=connectLive(token,read,update)
    control.current=connection
    const dispose=()=>{
      connection.close()
      if (control.current===connection) control.current=null
    }
    abort.signal.addEventListener("abort",dispose,{once:true})
    return ()=>{
      abort.abort()
      signals.delete(abort)
    }
  },[scene,token,epoch,signals,queryClient,liveKey])

  React.useEffect(()=>{
    if(!token || scene || !operating)return
    const abort=new AbortController()
    signals.add(abort)
    const update=(change:(state:LiveState)=>LiveState)=>{
      if(!abort.signal.aborted)queryClient.setQueryData<LiveState>(liveKey,state=>abort.signal.aborted ? state : change(state ?? initialLiveState()))
    }
    void seedRecentLiveEvents(async(path,signal)=>{
      for(let attempt=0;;attempt++) {
        signal.throwIfAborted()
        try {return (await relay(path,token,signal)).data}
        catch(error) {
          if(signal.aborted || !warmingRetry(attempt,error))throw error
          update(state=>["unauthorized","forbidden"].includes(state.status)?state:{...state,eventArchiveError:"초기화 중",eventArchiveErrorDetail:errorDetails(error)})
          signal.throwIfAborted()
          const {promise,resolve,reject}=Promise.withResolvers<void>()
          const cancel=()=>{clearTimeout(timer);reject(signal.reason)}
          const timer=setTimeout(()=>{signal.removeEventListener("abort",cancel);resolve()},warmingDelay(attempt,error))
          signal.addEventListener("abort",cancel,{once:true})
          await promise
        }
      }
    },update,abort.signal).catch(error=>{
      if(abort.signal.aborted)return
      if(error instanceof ApiError && error.status===401){window.dispatchEvent(new Event("chill-unauthorized"));return}
      update(state=>["unauthorized","forbidden"].includes(state.status) ? state : {...state,eventArchiveError:isCoverageWarming(error)?"초기화 중":"최근 사건 기록 조회 실패",eventArchiveErrorDetail:errorDetails(error)})
    })
    return ()=>{abort.abort();signals.delete(abort)}
  },[operating,scene,token,epoch,signals,queryClient,liveKey,archiveAttempt])

  const value=React.useMemo(()=>({state,status:state.status,reconnect}),[state,reconnect])
  return <LiveContext.Provider value={value}>
    {operating && state.eventArchiveError && <div role="status" className="notice">{state.eventArchiveError} <button type="button" onClick={()=>setArchiveAttempt(attempt=>attempt+1)}>다시 조회</button>{state.eventArchiveErrorDetail!==undefined&&<SourceJSON inline title="문제 자세히 보기" value={state.eventArchiveErrorDetail}/>}</div>}
    {children}
  </LiveContext.Provider>
}

export function useLive():LiveContextValue {
  const value=React.useContext(LiveContext)
  if (!value) throw new Error("LiveProvider is unavailable")
  return value
}

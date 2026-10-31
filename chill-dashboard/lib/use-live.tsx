"use client"

import * as React from "react"
import { skipToken, useQuery, useQueryClient } from "@tanstack/react-query"
import { useSearchParams } from "next/navigation"
import { useSession } from "@/lib/client"
import { applyLiveMessage, connectLive, initialLiveState, parseLiveMessage, type LiveConnection, type LiveState } from "@/lib/live"
import { fixtureMessages } from "@/lib/live-fixture"

type LiveContextValue = {state:LiveState;status:string;reconnect:()=>void}
const LiveContext=React.createContext<LiveContextValue|null>(null)

export function LiveProvider({children}:{children:React.ReactNode}) {
  const {token,epoch,config,signals}=useSession()
  const queryClient=useQueryClient()
  const search=useSearchParams()
  const requested=search.get("fixture")
  const scene=config.liveFixture && requested && ["empty","first","expand","shrink"].includes(requested) ? requested : null
  const liveKey=React.useMemo(()=>["chill-live",epoch,scene] as const,[epoch,scene])
  const {data}=useQuery<LiveState>({queryKey:liveKey,queryFn:skipToken,enabled:false,initialData:initialLiveState,staleTime:Infinity,gcTime:Infinity})
  // skipToken's overload permits undefined; initialData always seeds this cache-only query.
  const state=data!
  const control=React.useRef<LiveConnection|null>(null)
  const reconnect=React.useCallback(()=>control.current?.reconnect(),[])

  React.useEffect(()=>{
    if (!token) return
    const abort=new AbortController()
    signals.add(abort)
    const update=(change:(state:LiveState)=>LiveState)=>{queryClient.setQueryData<LiveState>(liveKey,state=>change(state ?? initialLiveState()));if(queryClient.getQueryData<LiveState>(liveKey)?.status==="unauthorized")window.dispatchEvent(new Event("chill-unauthorized"))}
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

  const value=React.useMemo(()=>({state,status:state.status,reconnect}),[state,reconnect])
  return <LiveContext.Provider value={value}>{children}</LiveContext.Provider>
}

export function useLive():LiveContextValue {
  const value=React.useContext(LiveContext)
  if (!value) throw new Error("LiveProvider is unavailable")
  return value
}

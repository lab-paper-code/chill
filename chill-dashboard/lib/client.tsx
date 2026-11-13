"use client"

import * as React from "react"
import { MutationCache, QueryCache, QueryClient, QueryClientProvider, useIsFetching, useIsMutating, useMutation, useQueries, useQuery } from "@tanstack/react-query"
import type { UseQueryOptions } from "@tanstack/react-query"
import { useRouter } from "next/navigation"
import type { PublicConfig } from "@/lib/server-config"
import type { ObservationResult } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { readTabToken, storeTabToken } from "@/lib/session-token"
import { ApiError, decodeResponse, relay, isCoverageWarming, warmingRetry, warmingDelay, type RelayResponse } from "@/lib/client-transport"

/** True while any query or mutation is waiting for ChillServer initialization (CoverageWarming). */
export function useInitializing():boolean {
  const queries=useIsFetching({predicate:query=>isCoverageWarming(query.state.fetchFailureReason)})
  const mutations=useIsMutating({predicate:mutation=>isCoverageWarming(mutation.state.failureReason)})
  return queries+mutations>0
}
const Session = React.createContext<{token:string; config:PublicConfig; epoch:number; signals:Set<AbortController>} | null>(null)
const Account = React.createContext<{signOut:()=>void} | null>(null)
/** Clears this tab's token and returns to the sign-in screen. */
export function useSignOut() {
  const account = React.useContext(Account)
  if (!account) throw new Error("Dashboard session is unavailable")
  return account.signOut
}
/** Props of the sign-in screen the provider renders while signed out. `pending`: the public config is still loading. */
export type SignInProps = {config:PublicConfig|null; configError:string; pending:boolean; onSubmit:(token:string)=>void}
export function useSession() {
  const session = React.useContext(Session)
  if (!session) throw new Error("Dashboard session is unavailable")
  return session
}
function clearUnauthorized(error:Error) {
  if(error instanceof ApiError&&error.status===401)window.dispatchEvent(new Event("chill-unauthorized"))
}

export function SessionProvider({children,signIn:SignIn}: {children:React.ReactNode;signIn:React.ComponentType<SignInProps>}) {
  const router = useRouter()
  const [client,setClient] = React.useState(() => new QueryClient({queryCache:new QueryCache({onError:clearUnauthorized}),mutationCache:new MutationCache({onError:clearUnauthorized}),defaultOptions:{queries:{retry:warmingRetry,retryDelay:warmingDelay,refetchOnWindowFocus:false,refetchOnReconnect:false},mutations:{retry:warmingRetry,retryDelay:warmingDelay}}}))
  const [signals] = React.useState(() => new Set<AbortController>())
  const [token,setToken] = React.useState("")
  const [epoch,setEpoch] = React.useState(0)
  const [config,setConfig] = React.useState<PublicConfig | null>(null)
  const [configError,setConfigError] = React.useState("")
  const [hydrated,setHydrated] = React.useState(false)
  const [,setStorageAvailable] = React.useState(true)
  React.useEffect(()=>{
    // New windows opened by this application never inherit an opener.
    const removeOpener=(event:MouseEvent)=>{const link=event.target instanceof Element?event.target.closest("a[target='_blank']"):null;if(link)link.setAttribute("rel","noopener noreferrer")}
    document.addEventListener("click",removeOpener,true)
    document.addEventListener("auxclick",removeOpener,true)
    return()=>{document.removeEventListener("click",removeOpener,true);document.removeEventListener("auxclick",removeOpener,true)}
  },[])
  React.useEffect(() => {
    const abort = new AbortController()
    fetch("/api/config",{cache:"no-store",signal:abort.signal}).then(async response => {
      if (!response.ok) throw new Error(`웹 설정 HTTP ${response.status}`)
      const next=await response.json() as PublicConfig
      setConfig(next)
      try {setToken(readTabToken(window.sessionStorage,next.systemName));setStorageAvailable(storeTabToken(window.sessionStorage,next.systemName,readTabToken(window.sessionStorage,next.systemName)))} catch {setStorageAvailable(false)}
      setHydrated(true)
    }).catch(error => {if (!abort.signal.aborted) setConfigError(error instanceof Error ? error.message : "웹 설정 조회 실패")})
    return () => abort.abort()
  },[])
  const changeIdentity = React.useCallback(async(next:string) => {
    if(config)try{setStorageAvailable(storeTabToken(window.sessionStorage,config.systemName,next))}catch{setStorageAvailable(false)}
    signals.forEach(controller => controller.abort())
    signals.clear()
    await client.cancelQueries()
    client.clear()
    if (token) {
      const url = new URL(window.location.href)
      if (url.pathname === "/records") {
        const selection = ["offset","at","id","node","startedAt"]
        const kind=url.searchParams.get("kind")
        if(kind&&!["requests","power","judgments"].includes(kind)){url.searchParams.delete("kind");url.searchParams.delete("cursor")}
        for (const key of selection) url.searchParams.delete(key)
        for (const prefix of ["a","b"]) for (const key of ["from","to","kind",...selection]) url.searchParams.delete(`${prefix}${key[0].toUpperCase()}${key.slice(1)}`)
        router.replace(`${url.pathname}${url.search}${url.hash}`,{scroll:false})
      }
    }
    // Detached old observers cannot repopulate the replacement identity's cache.
    setClient(new QueryClient({queryCache:new QueryCache({onError:clearUnauthorized}),mutationCache:new MutationCache({onError:clearUnauthorized}),defaultOptions:client.getDefaultOptions()}))
    setToken(next)
    setEpoch(value => value+1)
  },[client,config,router,signals,token])
  React.useEffect(()=>{const invalid=()=>{void changeIdentity("")};window.addEventListener("chill-unauthorized",invalid);return()=>window.removeEventListener("chill-unauthorized",invalid)},[changeIdentity])
  // Signed out shows only the sign-in screen; the shell appears after sign-in.
  return <QueryClientProvider client={client}>
    {!token ? <SignIn config={config} configError={configError} pending={!hydrated&&!configError} onSubmit={next => void changeIdentity(next.trim())}/>
    : config && <Session.Provider value={{token,config,epoch,signals}}>
      <Account.Provider value={{signOut:() => void changeIdentity("")}}>
        <React.Fragment key={epoch}>{children}</React.Fragment>
      </Account.Provider>
    </Session.Provider>}
  </QueryClientProvider>
}

function subscribeVisibility(notify:()=>void) {
  document.addEventListener("visibilitychange",notify)
  return () => document.removeEventListener("visibilitychange",notify)
}

export function useChillQuery<T>(path:string,options:{enabled?:boolean;poll?:boolean|((data:T|undefined,error:ApiError|null)=>boolean);decoder?:(body:string)=>unknown;onResponse?:(response:RelayResponse,data:T)=>void}={}) {
  const {token,config} = useSession()
  const visible = React.useSyncExternalStore(subscribeVisibility,() => document.visibilityState === "visible",() => true)
  const query = useQuery<T,ApiError>({queryKey:["chill",path],enabled:!!token && options.enabled !== false,queryFn:async ({signal}) => {
    const response = await relay(path,token,signal)
    const data=(options.decoder ? decodeResponse(response.body,options.decoder) : response.data) as T
    options.onResponse?.(response,data)
    return data
  },retry:warmingRetry,retryDelay:warmingDelay,refetchOnWindowFocus:false,refetchOnReconnect:false,refetchInterval:query => (typeof options.poll==="function"?options.poll(query.state.data,query.state.error):options.poll) && visible && ![401,403].includes(query.state.error?.status ?? 0) ? config.pollIntervalMs : false,refetchIntervalInBackground:false})
  return {...query,data:query.error && [401,403].includes(query.error.status) ? undefined : query.data}
}
export function useChillQueries<T>(paths:readonly string[],options:{enabled?:boolean;decoder?:(body:string)=>unknown}={}) {
  const {token}=useSession()
  return useQueries<Array<UseQueryOptions<T,ApiError>>>({queries:[...new Set(paths)].map(path=>({
    queryKey:["chill",path],enabled:!!token&&options.enabled!==false,
    queryFn:async ({signal}:{signal:AbortSignal}):Promise<T>=>{
      const response=await relay(path,token,signal)
      return (options.decoder?decodeResponse(response.body,options.decoder):response.data) as T
    },
    staleTime:Infinity,
  }))}).map(query=>({...query,data:query.error instanceof ApiError&&[401,403].includes(query.error.status)?undefined:query.data}))
}

// Load summaries omit definitions: read the retained catalog through the existing per-UID API.
export function useWorkloadDefinitions(uids:readonly string[],range:{from:string;to:string}) {
  const {token} = useSession()
  return useQueries({queries:[...new Set(uids)].filter(Boolean).map(uid => {
    const path = `v1/observations/workloads/${encodeURIComponent(uid)}?${new URLSearchParams({...range,limit:"1"})}`
    return {queryKey:["chill",path],enabled:!!token,queryFn:async ({signal}:{signal:AbortSignal}) => {
      const response = await relay(path,token,signal)
      return parseSchedulerJSON(response.body) as ObservationResult
    }}
  })}).flatMap(query => query.error instanceof ApiError && [401,403].includes(query.error.status) ? [] : query.data?.definitions ?? [])
}
export type ChillMutationInput = {path:string;method:"POST"|"PUT"|"DELETE";body?:unknown;version?:string;timeout?:string}
export function useChillMutation() {
  const {token,epoch,signals} = useSession()
  const scope=React.useRef<{token:string;epoch:number;controller:AbortController}|null>(null)
  React.useEffect(()=>{
    const current={token,epoch,controller:new AbortController()}
    scope.current=current
    signals.add(current.controller)
    return ()=>{current.controller.abort();signals.delete(current.controller)}
  },[token,epoch,signals])
  const active=()=>!!scope.current&&!scope.current.controller.signal.aborted&&scope.current.token===token&&scope.current.epoch===epoch
  return useMutation<RelayResponse,ApiError,ChillMutationInput>({retry:(attempt,error)=>active()&&warmingRetry(attempt,error),retryDelay:warmingDelay,mutationFn:async input => {
    if(!active())throw new ApiError(0,"요청 취소됨",null)
    if (!token) throw new ApiError(401,"인증 필요",null)
    return relay(input.path,token,scope.current!.controller.signal,input.method,input.body,input.version,input.timeout)
  }})
}

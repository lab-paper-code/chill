"use client"

import * as React from "react"
import { MutationCache, QueryCache, QueryClient, QueryClientProvider, useIsFetching, useIsMutating, useMutation, useQueries, useQuery } from "@tanstack/react-query"
import type { UseQueryOptions } from "@tanstack/react-query"
import { useRouter } from "next/navigation"
import type { PublicConfig } from "@/lib/server-config"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { BrandMark, FinField } from "@/components/brand"
import { KeyRound } from "lucide-react"
import type { ObservationResult } from "@/lib/read-types"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { readTabToken, storeTabToken } from "@/lib/session-token"
import { ApiError, decodeResponse, relay, isCoverageWarming, warmingRetry, warmingDelay, type RelayResponse } from "@/lib/client-transport"

function InitializationStatus() {
  const queries=useIsFetching({predicate:query=>isCoverageWarming(query.state.fetchFailureReason)})
  const mutations=useIsMutating({predicate:mutation=>isCoverageWarming(mutation.state.failureReason)})
  return queries+mutations>0?<div role="status" className="notice">초기화 중</div>:null
}
const Session = React.createContext<{token:string; config:PublicConfig; epoch:number; signals:Set<AbortController>} | null>(null)
const Account = React.createContext<{open:boolean; setOpen:(open:boolean)=>void; form:React.ReactNode; signOut:()=>void} | null>(null)
export function useSession() {
  const session = React.useContext(Session)
  if (!session) throw new Error("Dashboard session is unavailable")
  return session
}
function clearUnauthorized(error:Error) {
  if(error instanceof ApiError&&error.status===401)window.dispatchEvent(new Event("chill-unauthorized"))
}

export function SessionProvider({children}: {children:React.ReactNode}) {
  const router = useRouter()
  const [client,setClient] = React.useState(() => new QueryClient({queryCache:new QueryCache({onError:clearUnauthorized}),mutationCache:new MutationCache({onError:clearUnauthorized}),defaultOptions:{queries:{retry:warmingRetry,retryDelay:warmingDelay,refetchOnWindowFocus:false,refetchOnReconnect:false},mutations:{retry:warmingRetry,retryDelay:warmingDelay}}}))
  const [signals] = React.useState(() => new Set<AbortController>())
  const [token,setToken] = React.useState("")
  const [epoch,setEpoch] = React.useState(0)
  const [draft,setDraft] = React.useState("")
  const [accountOpen,setAccountOpen] = React.useState(false)
  const [config,setConfig] = React.useState<PublicConfig | null>(null)
  const [configError,setConfigError] = React.useState("")
  const [hydrated,setHydrated] = React.useState(false)
  const [storageAvailable,setStorageAvailable] = React.useState(true)
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
    setDraft("")
    setAccountOpen(false)
    setToken(next)
    setEpoch(value => value+1)
  },[client,config,router,signals,token])
  React.useEffect(()=>{const invalid=()=>{void changeIdentity("")};window.addEventListener("chill-unauthorized",invalid);return()=>window.removeEventListener("chill-unauthorized",invalid)},[changeIdentity])
  const signInForm = <form onSubmit={event => {event.preventDefault(); void changeIdentity(draft.trim())}} className="grid gap-4">
    <label className="grid gap-2 text-sm font-medium" htmlFor="bearer-token">접속 토큰
      <Input id="bearer-token" type="password" autoComplete="off" spellCheck={false} value={draft} onChange={event => setDraft(event.target.value)} placeholder="관리자에게 받은 토큰을 붙여 넣으세요" required />
    </label>
    <Button type="submit" size="lg" disabled={!config || !draft.trim()}><KeyRound />{token ? "접속 정보 바꾸기" : "시작하기"}</Button>
    <table className="w-full table-fixed border-collapse text-sm" aria-label="토큰 보관·유효 범위"><tbody>{[
      ["보관 위치",storageAvailable?"이 시스템·현재 탭":"메모리 전용"],
      ["새로고침",storageAvailable?"이 탭에서 복원":"다시 입력 필요"],
      ["탭 복제·브라우저 복원",storageAvailable?"토큰이 복사될 수 있음":"보관 보장 없음"],
      ["이 탭 삭제 영향","현재 탭에 보관된 토큰만 삭제"],
      ["다른 탭·토큰 자체","폐기되지 않음"],
      ["탭 닫기","안전 삭제 보장 없음"],
      ["토큰 유효기간","미제공"],
    ].map(([label,value])=><tr key={label} className="border-b"><th scope="row" className="w-1/2 break-keep px-2 py-2 text-left font-normal text-muted-foreground">{label}</th><td className="break-keep px-2 py-2">{value}</td></tr>)}</tbody></table>
  </form>
  // Signed out shows only the sign-in screen; the shell appears after sign-in.
  return <QueryClientProvider client={client}>
    {!hydrated&&!configError?<main className="screen" role="status">접속 설정 확인 중</main>:!token ? <div className="login">
      <section className="login-brand" aria-label="CHILL 소개">
        <span className="flex items-center gap-2.5 text-lg font-bold"><BrandMark className="size-7 text-[#5fc2e8]" />CHILL</span>
        <div className="relative grid gap-4"><h1>응답 목표를 지키면서 에너지를 덜 쓰는 구성을 찾아요</h1><p>모델과 서비스를 등록하고, 실행 그룹의 실제 구성과 운영 판단이 예상한 에너지를 한곳에서 확인하세요.</p></div>
        {config ? <div className="login-target relative" aria-label="접속 대상"><span>대상 {config.systemName}</span><span>네임스페이스 {config.namespace}</span></div> : <span />}
        <FinField className="login-fins" />
      </section>
      <main className="login-form" id="main-content"><h2>접속하기</h2>{signInForm}{configError && <p role="alert">접속 설정을 불러오지 못했어요. 잠시 후 다시 확인해 주세요.</p>}</main>
    </div> : config && <Session.Provider value={{token,config,epoch,signals}}>
      <Account.Provider value={{open:accountOpen,setOpen:open => {setAccountOpen(open); if (!open) setDraft("")},form:signInForm,signOut:() => void changeIdentity("")}}>
        <React.Fragment key={epoch}><InitializationStatus/>{children}</React.Fragment>
      </Account.Provider>
    </Session.Provider>}
  </QueryClientProvider>
}

export function AccountControl() {
  const account = React.useContext(Account)
  const {config} = useSession()
  if (!account) return null
  return <Dialog open={account.open} onOpenChange={account.setOpen}>
    <DialogTrigger asChild><Button variant="outline" size="sm" className="gap-2 rounded-full"><span className="size-2 rounded-full bg-ok" aria-hidden="true" /><span className="max-w-40 truncate">{config.systemName}</span><span className="hidden text-muted-foreground sm:inline">{config.namespace}</span><span className="sr-only">접속 관리</span></Button></DialogTrigger>
    <DialogContent aria-describedby={undefined}><DialogHeader><DialogTitle>접속 관리</DialogTitle></DialogHeader>
      <dl className="grid grid-cols-2 gap-x-4 rounded-lg bg-muted p-3 text-sm"><div><dt>대상</dt><dd>{config.systemName}</dd></div><div><dt>네임스페이스</dt><dd>{config.namespace}</dd></div></dl>
      {account.form}<Button variant="outline" onClick={account.signOut}>이 탭에서 토큰 지우기</Button>
    </DialogContent>
  </Dialog>
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

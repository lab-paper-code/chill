"use client"

import * as React from "react"
import { QueryClient, QueryClientProvider, useMutation, useQuery } from "@tanstack/react-query"
import type { PublicConfig } from "@/lib/server-config"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"

export type RelayResponse = { status: number; body: string; data: unknown; requestID: string | null; workloadUID: string | null; relayTiming: string | null; browserRTT: number }
export class ApiError extends Error {
  constructor(public status: number, public body: string, public data: unknown, public unknownOutcome = false, public requestID: string | null = null, public workloadUID: string | null = null, public relayTiming: string | null = null, public browserRTT: number | null = null) {
    super(status ? `HTTP ${status}` : "결과 미확인 · 응답 미수신")
    this.name = "ApiError"
  }
}
const Session = React.createContext<{token:string; config:PublicConfig; epoch:number; signals:Set<AbortController>} | null>(null)
export function useSession() {
  const session = React.useContext(Session)
  if (!session) throw new Error("Dashboard session is unavailable")
  return session
}

export function SessionProvider({children}: {children:React.ReactNode}) {
  const [client,setClient] = React.useState(() => new QueryClient({defaultOptions:{queries:{retry:false,refetchOnWindowFocus:false,refetchOnReconnect:false},mutations:{retry:false}}}))
  const [signals] = React.useState(() => new Set<AbortController>())
  const [token,setToken] = React.useState("")
  const [epoch,setEpoch] = React.useState(0)
  const [draft,setDraft] = React.useState("")
  const [accountOpen,setAccountOpen] = React.useState(false)
  const [config,setConfig] = React.useState<PublicConfig | null>(null)
  const [configError,setConfigError] = React.useState("")
  React.useEffect(() => {
    const abort = new AbortController()
    fetch("/api/config",{cache:"no-store",signal:abort.signal}).then(async response => {
      if (!response.ok) throw new Error(`웹 설정 HTTP ${response.status}`)
      setConfig(await response.json() as PublicConfig)
    }).catch(error => {if (!abort.signal.aborted) setConfigError(error instanceof Error ? error.message : "웹 설정 조회 실패")})
    return () => abort.abort()
  },[])
  async function changeIdentity(next:string) {
    signals.forEach(controller => controller.abort())
    signals.clear()
    await client.cancelQueries()
    client.clear()
    // Detached old observers cannot repopulate the replacement identity's cache.
    setClient(new QueryClient({defaultOptions:client.getDefaultOptions()}))
    setDraft("")
    setAccountOpen(false)
    setToken(next)
    setEpoch(value => value+1)
  }
  const signInForm = <form onSubmit={event => {event.preventDefault(); void changeIdentity(draft.trim())}} className="grid gap-5">
    <label className="grid gap-2" htmlFor="bearer-token">접속 토큰
      <Input id="bearer-token" type="password" autoComplete="off" spellCheck={false} value={draft} onChange={event => setDraft(event.target.value)} placeholder="관리자에게 받은 토큰을 붙여 넣으세요" required />
    </label>
    <Button type="submit" disabled={!config || !draft.trim()}>{token ? "접속 정보 바꾸기" : "시작하기"}</Button>
    <p className="text-sm text-muted-foreground">접속 정보는 이 탭에서만 사용해요. 새로고침하면 다시 입력해야 해요.</p>
  </form>
  return <QueryClientProvider client={client}>
    {!token ? <section className="login-page" aria-label="접속">
      <span className="eyebrow">CHILL</span><h1>CHILL에 접속하기</h1>
      <p className="text-muted-foreground">접속 토큰으로 시작해 주세요.</p>
      {signInForm}
      {configError && <p role="alert">접속 설정을 불러오지 못했어요. 잠시 후 다시 확인해 주세요.</p>}
    </section> : <>
      <section className="auth-bar" aria-label="접속 관리">
        <span className="text-sm text-muted-foreground">{config?.systemName}</span>
        <Dialog open={accountOpen} onOpenChange={open => {setAccountOpen(open); if (!open) setDraft("")}}>
          <DialogTrigger asChild><Button variant="ghost">접속 관리</Button></DialogTrigger>
          <DialogContent><DialogHeader><DialogTitle>접속 관리</DialogTitle><DialogDescription>다른 접속 정보로 바꾸거나 안전하게 나갈 수 있어요.</DialogDescription></DialogHeader>
            {signInForm}<Button variant="outline" onClick={() => void changeIdentity("")}>로그아웃</Button>
            {config && <p className="text-xs text-muted-foreground">대상 {config.systemName} · namespace {config.namespace}</p>}
          </DialogContent>
        </Dialog>
      </section>
      {config && <Session.Provider value={{token,config,epoch,signals}}><React.Fragment key={epoch}>{children}</React.Fragment></Session.Provider>}
    </>}
  </QueryClientProvider>
}

async function relay(path:string,token:string,signal:AbortSignal,method="GET",body?:unknown,version?:string,timeout?:string):Promise<RelayResponse> {
  let response:Response | null = null
  let text:string
  const started = performance.now()
  try {
    response = await fetch(`/api/chill/${path}`,{method,signal,cache:"no-store",credentials:"omit",headers:{Authorization:`Bearer ${token}`,...(body === undefined ? {} : {"Content-Type":"application/json"}),...(version ? {"If-Match":version} : {}),...(timeout ? {"X-Chill-Request-Timeout":timeout} : {})},body:body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body)})
    text = await response.text()
  } catch {
    throw new ApiError(response?.status ?? 0,"응답 본문 미수신 · 원본 상태를 조회한 뒤 명시적으로 재제출하세요",null,method !== "GET" && !path.endsWith("/preflight"),response?.headers.get("X-Chill-Request-ID") ?? null,response?.headers.get("X-Chill-Workload-UID") ?? null,response?.headers.get("X-Chill-Relay-Timing") ?? null,performance.now()-started)
  }
  const browserRTT = performance.now()-started
  let data:unknown = text
  try {data = text ? JSON.parse(text) : null} catch { /* native response may be plain text */ }
  if (!response.ok) {
    const unknown = method !== "GET" && !!(data && typeof data === "object" && "unknownOutcome" in data && data.unknownOutcome)
    throw new ApiError(response.status,text,data,unknown,response.headers.get("X-Chill-Request-ID"),response.headers.get("X-Chill-Workload-UID"),response.headers.get("X-Chill-Relay-Timing"),browserRTT)
  }
  return {status:response.status,body:text,data,requestID:response.headers.get("X-Chill-Request-ID"),workloadUID:response.headers.get("X-Chill-Workload-UID"),relayTiming:response.headers.get("X-Chill-Relay-Timing"),browserRTT}
}

function subscribeVisibility(notify:()=>void) {
  document.addEventListener("visibilitychange",notify)
  return () => document.removeEventListener("visibilitychange",notify)
}

export function useChillQuery<T>(path:string,options:{enabled?:boolean;poll?:boolean}={}) {
  const {token,config} = useSession()
  const visible = React.useSyncExternalStore(subscribeVisibility,() => document.visibilityState === "visible",() => true)
  const query = useQuery<T,ApiError>({queryKey:["chill",path],enabled:!!token && options.enabled !== false,queryFn:async ({signal}) => (await relay(path,token,signal)).data as T,retry:false,refetchOnWindowFocus:false,refetchOnReconnect:false,refetchInterval:query => options.poll && visible && ![401,403].includes(query.state.error?.status ?? 0) ? config.pollIntervalMs : false,refetchIntervalInBackground:false})
  return {...query,data:query.error && [401,403].includes(query.error.status) ? undefined : query.data}
}
export function useChillMutation() {
  const {token,signals} = useSession()
  return useMutation<RelayResponse,ApiError,{path:string;method:"POST"|"PUT"|"DELETE";body?:unknown;version?:string;timeout?:string}>({retry:false,mutationFn:async input => {
    if (!token) throw new ApiError(401,"인증 필요",null)
    const controller = new AbortController()
    signals.add(controller)
    try { return await relay(input.path,token,controller.signal,input.method,input.body,input.version,input.timeout) }
    finally {signals.delete(controller)}
  }})
}

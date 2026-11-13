import { parseMeasurementJSON } from "@/lib/measurement-summary"

export type RelayResponse = { status: number; body: string; data: unknown; requestID: string | null; workloadUID: string | null; relayTiming: string | null; browserRTT: number; etag?:string|null }
export class ApiError extends Error {
  requestPath?: string
  originalError?: {name:string;message:string}
  constructor(public status: number, public body: string, public data: unknown, public unknownOutcome = false, public requestID: string | null = null, public workloadUID: string | null = null, public relayTiming: string | null = null, public browserRTT: number | null = null, public retryAfter: number = 2) {
    super(isCoverageWarming({status,data}) ? "초기화 중" : status ? `HTTP ${status}` : "결과 미확인, 응답 미수신")
    this.name = "ApiError"
  }
}
export function errorDetails(error:unknown) {
  if(error instanceof ApiError) {
    const data=error.data as {code?:unknown;message?:unknown}|null
    return {status:error.status,code:typeof data?.code==="string"?data.code:undefined,message:typeof data?.message==="string"?data.message:undefined,exception:error.originalError,requestPath:error.requestPath}
  }
  return error instanceof Error?{exception:{name:error.name,message:error.message}}:undefined
}
export function isCoverageWarming(error:unknown):boolean {
  const value=error as {status?:number;data?:{code?:unknown}}|null
  return value?.status===503&&value.data?.code==="CoverageWarming"
}
export const warmingRetry=(attempt:number,error:unknown)=>isCoverageWarming(error)&&attempt<180
export const warmingDelay=(_attempt:number,error:unknown)=>Math.min(5,Math.max(1,error instanceof ApiError&&Number.isFinite(error.retryAfter)?error.retryAfter:2))*1000

export function decodeResponse(body:string,decoder:(body:string)=>unknown):unknown {
  try {return decoder(body)}
  catch(error) {
    const message=error instanceof Error?error.message:"응답 해석 실패"
    throw new ApiError(0,message,{code:decoder===parseMeasurementJSON?"MeasurementSummaryInvalid":"DashboardPrecisionUnsupported",message})
  }
}

export async function relay(path:string,token:string,signal:AbortSignal,method="GET",body?:unknown,version?:string,timeout?:string):Promise<RelayResponse> {
  let response:Response | null = null
  let text:string
  const started = performance.now()
  try {
    response = await fetch(`/api/chill/${path}`,{method,signal,cache:"no-store",credentials:"omit",headers:{Authorization:`Bearer ${token}`,...(body === undefined ? {} : {"Content-Type":"application/json"}),...(version ? {"If-Match":version} : {}),...(timeout ? {"X-Chill-Request-Timeout":timeout} : {})},body:body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body)})
    text = await response.text()
  } catch (error) {
    throw Object.assign(new ApiError(response?.status ?? 0,"응답 본문 미수신, 원본 상태를 조회한 뒤 명시적으로 재제출하세요",null,method !== "GET" && !path.endsWith("/preflight") && !path.endsWith("?dryRun=All"),response?.headers.get("X-Chill-Request-ID") ?? null,response?.headers.get("X-Chill-Workload-UID") ?? null,response?.headers.get("X-Chill-Relay-Timing") ?? null,performance.now()-started),{requestPath:new URL(path,"https://dashboard.invalid/").pathname,originalError:error instanceof Error?{name:error.name,message:token?error.message.replaceAll(token,"[redacted]"):error.message}:undefined})
  }
  const browserRTT = performance.now()-started
  let data:unknown = text
  try {data = text ? JSON.parse(text) : null} catch { /* native response may be plain text */ }
  if (!response.ok) {
    if(method==="GET"&&response.status===400&&data&&typeof data==="object"&&"code" in data&&["InvalidQuery","InvalidCoverageQuery"].includes(String(data.code))) {
      const legacy=new URL(path,"https://dashboard.invalid/")
      if(legacy.searchParams.get("summary")==="v2") {
        legacy.searchParams.delete("summary")
        if(/^\/v1\/namespaces\/[^/]+\/batch-energy-optimizations$/.test(legacy.pathname)) {
          legacy.searchParams.delete("limit");legacy.searchParams.delete("offset")
        }
        return relay(`${legacy.pathname.slice(1)}?${legacy.searchParams}`,token,signal)
      }
    }
    const unknown = method !== "GET" && !!(data && typeof data === "object" && "unknownOutcome" in data && data.unknownOutcome)
    const retryAfter=response.headers.get("Retry-After"),seconds=retryAfter===null?2:Number(retryAfter)
    throw Object.assign(new ApiError(response.status,text,data,unknown,response.headers.get("X-Chill-Request-ID"),response.headers.get("X-Chill-Workload-UID"),response.headers.get("X-Chill-Relay-Timing"),browserRTT,Number.isFinite(seconds)?seconds:2),{requestPath:new URL(path,"https://dashboard.invalid/").pathname})
  }
  return {status:response.status,body:text,data,requestID:response.headers.get("X-Chill-Request-ID"),workloadUID:response.headers.get("X-Chill-Workload-UID"),relayTiming:response.headers.get("X-Chill-Relay-Timing"),browserRTT,etag:response.headers.get("ETag")}
}

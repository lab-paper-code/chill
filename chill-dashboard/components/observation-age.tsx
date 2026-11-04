import type { LiveState } from "@/lib/live"
import "./observation-age.css"

export function ObservationAge({at,now,label="관측"}:{at:string|number|null|undefined;now:number;label?:string}) {
  const time=typeof at==="number"?at:at?Date.parse(at):NaN,seconds=Number.isFinite(time)?Math.max(0,Math.floor((now-time)/1000)):null
  return <span className="observation-age" title={Number.isFinite(time)?new Date(time).toISOString():undefined}><span>{label}</span><span className="observation-age-elapsed"><span className="observation-age-value">{seconds===null?"미수신":seconds.toLocaleString("ko-KR")}</span><span className="observation-age-unit">{seconds===null?"":"초 전"}</span></span></span>
}

export function LiveObservation({state,now,status=state.status,paused=false}:{state:LiveState;now:number;status?:string;paused?:boolean}) {
  const labels:Record<string,string>={connecting:"연결 중",resuming:"재개 중",live:"실시간",reconnecting:"재연결 중",disconnected:"연결 끊김",unauthorized:"인증 필요",forbidden:"권한 없음","protocol-error":"계약 오류",idle:"연결 대기",fixture:"검사용","paused-hidden":"탭 일시정지"}
  // A heartbeat is transport activity, never a replacement for a metric observation.
  return <span className="live-observation"><span className="observation-connection" data-connected={!paused&&(status==="live"||status==="fixture")}><i aria-hidden="true"/>{paused?"화면 일시정지":labels[status]??status}</span><ObservationAge at={state.metrics.at(-1)?.bucketAt} now={now}/></span>
}

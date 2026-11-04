import type { MouseEventHandler } from "react"
import type { LiveInstance } from "@/lib/live"
import { instanceSymbol, instanceSymbols, routingShare } from "@/lib/live-display"
import "./serving-instance.css"

export function ServingInstance({instance,workloadUID,weights,name,powerMode,onClick,index}:{instance:LiveInstance;workloadUID:string;weights?:Record<string,number>;name:string;powerMode?:string;onClick?:MouseEventHandler<HTMLButtonElement>;index?:number}) {
  const symbol=instanceSymbol(instance,workloadUID),state=instanceSymbols.indexOf(symbol),share=routingShare(weights,instance.id)
  const content=<>
    <span className="serving-instance-heading">{index!=null&&<span className="serving-instance-index" aria-label={`인스턴스 ${index+1}`}>{index+1}</span>}<strong>{name||"이름 미제공"}</strong><span className="serving-instance-share">{share==null?"미확인":`${share.toFixed(1)}%`}</span></span>
    <span className="serving-instance-state"><span className="serving-instance-symbol" aria-hidden="true">{symbol[0]}</span>{symbol[1]}</span>
    <span className="serving-instance-facts"><span aria-label="처리 요청">처리 중 <b>{instance.running[workloadUID]??0}</b></span><span aria-label="대기 요청">대기 <b>{instance.waiting[workloadUID]??0}</b></span>{powerMode!==undefined&&<span className="serving-instance-mode">{powerMode||"모드 미확인"}</span>}</span>
  </>
  return onClick?<button type="button" className="serving-instance" data-instance-state={state} onClick={onClick}>{content}</button>:<div className="serving-instance" data-instance-state={state}>{content}</div>
}

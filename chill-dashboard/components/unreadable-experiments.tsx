import type { ExperimentPage } from "@/lib/experiments"

export function UnreadableExperiments({unreadable}:{unreadable:ExperimentPage["unreadable"]}) {
  if(!Array.isArray(unreadable)||unreadable.length===0)return null
  return <details className="min-w-0 rounded border border-destructive/40 p-3 text-sm">
    <summary className="min-h-11 cursor-pointer font-medium" title="게시 요약이 있지만 서버에서 읽지 못해 목록에서 제외된 실험입니다. 건수는 현재 페이지가 아닌 전체 게시 기준입니다.">{unreadable.length}건 읽을 수 없음</summary>
    <table className="w-full table-fixed text-left"><thead><tr><th scope="col" className="w-1/3 p-2">식별자</th><th scope="col" className="p-2">사유</th></tr></thead><tbody>{unreadable.map(item=><tr key={item.id} className="border-t align-top"><th scope="row" className="break-all p-2 font-normal">{item.id}</th><td className="break-words p-2">{item.reason}</td></tr>)}</tbody></table>
  </details>
}

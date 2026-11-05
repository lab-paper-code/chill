"use client"

import { Button } from "@/components/ui/button"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import type { MeasurementRequestDocument } from "@/lib/measurement-document"

// Native requests must round-trip without the generic evidence export's key redaction.
export function MeasurementRequestSource({value,title="제출할 전체 요청 보기"}:{value:MeasurementRequestDocument;title?:string}) {
  const text=stringifySchedulerJSON(value,2)
  return <details className="min-w-0">
    <summary className="min-h-11 cursor-pointer py-2">{title}</summary>
    <pre className="source-json">{text}</pre>
    <p className="py-3 text-sm text-muted-foreground">재사용할 요청 원문을 그대로 저장해요. 원문에 민감정보가 있으면 파일에도 포함돼요.</p>
    <Button type="button" variant="outline" onClick={()=>{
      const url=URL.createObjectURL(new Blob([text],{type:"application/json"}))
      const link=document.createElement("a")
      link.href=url;link.download=`${value.metadata.name}-request.json`.replace(/[^a-zA-Z0-9._-]/g,"_")
      link.click();URL.revokeObjectURL(url)
    }}>요청 문서 다운로드</Button>
  </details>
}

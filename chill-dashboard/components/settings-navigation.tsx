"use client"

import Link from "next/link"

export type SettingsTab = "configuration" | "models" | "preprocessors" | "datasets"
export function SettingsNavigation({active}:{active:SettingsTab}) {
  return <nav aria-label="설정" className="flex flex-wrap gap-2">{([["configuration","운영 설정"],["models","모델"],["preprocessors","입력 처리"],["datasets","데이터셋"]] as const).map(([tab,label])=><Link key={tab} href={`/settings?tab=${tab}`} aria-current={active===tab?"page":undefined} className={`inline-flex min-h-11 items-center rounded-md border px-4 text-sm ${active===tab?"bg-primary text-primary-foreground":"hover:bg-muted"}`}>{label}</Link>)}</nav>
}

"use client"

import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function ExperimentResultDetailScreen({ id }: { id: string }) {
  return (
    <Page>
      <PageHeader title={id} />
      <PageTabs {...{ param: "tab", defaultValue: "summary", tabs: [{ value: "summary", label: "요약" }, { value: "trend", label: "추이" }, { value: "requests", label: "요청 결과" }, { value: "energy", label: "에너지" }, { value: "conditions", label: "조건" }] }} />
      <NotReady />
    </Page>
  )
}

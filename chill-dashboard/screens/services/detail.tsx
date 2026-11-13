"use client"

import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function ServiceDetailScreen({ name }: { name: string }) {
  return (
    <Page>
      <PageHeader title={name} />
      <PageTabs {...{ param: "tab", defaultValue: "overview", tabs: [{ value: "overview", label: "현황" }, { value: "requests", label: "요청" }, { value: "definition", label: "정의" }, { value: "test", label: "테스트" }] }} />
      <NotReady />
    </Page>
  )
}

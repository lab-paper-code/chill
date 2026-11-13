"use client"

import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function ExperimentRunDetailScreen({ name }: { name: string }) {
  return (
    <Page>
      <PageHeader title={name} />
      <PageTabs {...{ param: "tab", defaultValue: "progress", tabs: [{ value: "progress", label: "진행" }, { value: "plan", label: "계획" }, { value: "cleanup", label: "정리" }, { value: "target", label: "대상" }] }} />
      <NotReady />
    </Page>
  )
}

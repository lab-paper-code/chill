"use client"

import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function MeasurementRequestDetailScreen({ name }: { name: string }) {
  return (
    <Page>
      <PageHeader title={name} />
      <PageTabs {...{ param: "tab", defaultValue: "results", tabs: [{ value: "results", label: "결과" }, { value: "runs", label: "회차" }, { value: "conditions", label: "조건" }] }} />
      <NotReady />
    </Page>
  )
}

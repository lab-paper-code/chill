"use client"

import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function MeasurementRequestNewScreen() {
  return (
    <Page>
      <PageHeader title={"측정 요청"} />
      <NotReady />
    </Page>
  )
}

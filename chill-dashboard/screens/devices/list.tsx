"use client"

import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function DeviceListScreen() {
  return (
    <Page>
      <PageHeader title={"장치"} />
      <NotReady />
    </Page>
  )
}

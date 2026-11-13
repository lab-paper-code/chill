"use client"

import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function ServiceListScreen() {
  return (
    <Page>
      <PageHeader title={"서비스"} />
      <NotReady />
    </Page>
  )
}

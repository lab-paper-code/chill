"use client"

import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function OverviewScreen() {
  return (
    <Page>
      <PageHeader title={"개요"} />
      <NotReady />
    </Page>
  )
}

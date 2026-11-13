"use client"

import { sectionTabs } from "@/lib/routes"
import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function DecisionsScreen() {
  return (
    <Page>
      <PageHeader title={"스케줄러"} />
      <PageTabs {...{ tabs: sectionTabs.scheduler }} />
      <NotReady />
    </Page>
  )
}

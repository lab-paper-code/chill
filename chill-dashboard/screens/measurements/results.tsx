"use client"

import { sectionTabs } from "@/lib/routes"
import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function MeasurementResultsScreen() {
  return (
    <Page>
      <PageHeader title={"측정"} />
      <PageTabs {...{ tabs: sectionTabs.measurements }} />
      <NotReady />
    </Page>
  )
}

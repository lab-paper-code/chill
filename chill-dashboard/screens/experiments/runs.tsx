"use client"

import { sectionTabs } from "@/lib/routes"
import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function ExperimentRunsScreen() {
  return (
    <Page>
      <PageHeader title={"실험"} />
      <PageTabs {...{ tabs: sectionTabs.experiments }} />
      <NotReady />
    </Page>
  )
}

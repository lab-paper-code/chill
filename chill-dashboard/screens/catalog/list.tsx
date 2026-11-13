"use client"

import { sectionTabs, type CatalogKind } from "@/lib/routes"
import { NotReady, Page, PageHeader, PageTabs } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function CatalogListScreen({ kind }: { kind: CatalogKind }) {
  void kind
  return (
    <Page>
      <PageHeader title={"카탈로그"} />
      <PageTabs {...{ tabs: sectionTabs.catalog }} />
      <NotReady />
    </Page>
  )
}

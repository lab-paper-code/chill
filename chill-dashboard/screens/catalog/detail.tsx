"use client"

import { type CatalogKind } from "@/lib/routes"
import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function CatalogDetailScreen({ kind, name }: { kind: CatalogKind; name: string }) {
  void kind
  return (
    <Page>
      <PageHeader title={name} />
      <NotReady />
    </Page>
  )
}

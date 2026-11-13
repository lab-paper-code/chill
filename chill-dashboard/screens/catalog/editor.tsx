"use client"

import { type CatalogKind, catalogKinds } from "@/lib/routes"
import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function CatalogEditorScreen({ kind, name }: { kind: CatalogKind; name?: string }) {
  return (
    <Page>
      <PageHeader title={name ? `${name} 수정` : `${catalogKinds[kind]} 등록`} />
      <NotReady />
    </Page>
  )
}

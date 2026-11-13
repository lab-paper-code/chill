"use client"

import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function ServiceEditorScreen({ name }: { name?: string }) {
  return (
    <Page>
      <PageHeader title={name ? `${name} 수정` : "서비스 등록"} />
      <NotReady />
    </Page>
  )
}

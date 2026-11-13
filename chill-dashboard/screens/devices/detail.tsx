"use client"

import { NotReady, Page, PageHeader } from "@/components/kit"

// Placeholder: the screen owner replaces the body (design section 7).
export function DeviceDetailScreen({ name }: { name: string }) {
  return (
    <Page>
      <PageHeader title={name} />
      <NotReady />
    </Page>
  )
}

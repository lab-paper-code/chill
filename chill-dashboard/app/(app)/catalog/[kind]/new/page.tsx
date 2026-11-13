import { notFound } from "next/navigation"
import { decodeSegment, isCatalogKind, catalogKinds, type CatalogKind } from "@/lib/routes"
import { CatalogEditorScreen } from "@/screens/catalog/editor"

type Props = { params: Promise<{ kind: string }> }

export async function generateMetadata({ params }: Props) {
  const { kind } = await params
  return { title: `${catalogKinds[kind as CatalogKind] ?? "카탈로그"} 등록` }
}

export default async function Page({ params }: Props) {
  const { kind } = await params
  if (!isCatalogKind(kind)) notFound()
  return <CatalogEditorScreen kind={kind} />
}

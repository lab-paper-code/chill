import { notFound } from "next/navigation"
import { decodeSegment, isCatalogKind } from "@/lib/routes"
import { CatalogEditorScreen } from "@/screens/catalog/editor"

type Props = { params: Promise<{ kind: string; name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: `${decodeSegment(name)} 수정` }
}

export default async function Page({ params }: Props) {
  const { kind, name } = await params
  if (!isCatalogKind(kind)) notFound()
  return <CatalogEditorScreen kind={kind} name={decodeSegment(name)} />
}

import { notFound } from "next/navigation"
import { decodeSegment, isCatalogKind } from "@/lib/routes"
import { CatalogDetailScreen } from "@/screens/catalog/detail"

type Props = { params: Promise<{ kind: string; name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: decodeSegment(name) }
}

export default async function Page({ params }: Props) {
  const { kind, name } = await params
  if (!isCatalogKind(kind)) notFound()
  return <CatalogDetailScreen kind={kind} name={decodeSegment(name)} />
}

import { decodeSegment } from "@/lib/routes"
import { ScenarioDetailScreen } from "@/screens/experiments/scenario-detail"

type Props = { params: Promise<{ name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: decodeSegment(name) }
}

export default async function Page({ params }: Props) {
  const { name } = await params
  return <ScenarioDetailScreen name={decodeSegment(name)} />
}

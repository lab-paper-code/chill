import { decodeSegment } from "@/lib/routes"
import { ExperimentRunDetailScreen } from "@/screens/experiments/run-detail"

type Props = { params: Promise<{ name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: decodeSegment(name) }
}

export default async function Page({ params }: Props) {
  const { name } = await params
  return <ExperimentRunDetailScreen name={decodeSegment(name)} />
}

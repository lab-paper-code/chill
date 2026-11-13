import { decodeSegment } from "@/lib/routes"
import { ExperimentResultDetailScreen } from "@/screens/experiments/result-detail"

type Props = { params: Promise<{ id: string }> }

export async function generateMetadata({ params }: Props) {
  const { id } = await params
  return { title: decodeSegment(id) }
}

export default async function Page({ params }: Props) {
  const { id } = await params
  return <ExperimentResultDetailScreen id={decodeSegment(id)} />
}

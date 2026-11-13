import { decodeSegment } from "@/lib/routes"
import { MeasurementRequestDetailScreen } from "@/screens/measurements/request-detail"

type Props = { params: Promise<{ name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: decodeSegment(name) }
}

export default async function Page({ params }: Props) {
  const { name } = await params
  return <MeasurementRequestDetailScreen name={decodeSegment(name)} />
}

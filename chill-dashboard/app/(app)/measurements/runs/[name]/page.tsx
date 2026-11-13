import { decodeSegment } from "@/lib/routes"
import { MeasurementRunScreen } from "@/screens/measurements/run-detail"

type Props = { params: Promise<{ name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: decodeSegment(name) }
}

export default async function Page({ params }: Props) {
  const { name } = await params
  return <MeasurementRunScreen name={decodeSegment(name)} />
}

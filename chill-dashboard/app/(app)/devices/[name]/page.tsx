import { decodeSegment } from "@/lib/routes"
import { DeviceDetailScreen } from "@/screens/devices/detail"

type Props = { params: Promise<{ name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: decodeSegment(name) }
}

export default async function Page({ params }: Props) {
  const { name } = await params
  return <DeviceDetailScreen name={decodeSegment(name)} />
}

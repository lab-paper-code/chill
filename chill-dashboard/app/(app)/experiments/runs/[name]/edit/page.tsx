import { decodeSegment } from "@/lib/routes"
import { ExperimentEditorScreen } from "@/screens/experiments/run-editor"

type Props = { params: Promise<{ name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: `${decodeSegment(name)} 수정` }
}

export default async function Page({ params }: Props) {
  const { name } = await params
  return <ExperimentEditorScreen name={decodeSegment(name)} />
}

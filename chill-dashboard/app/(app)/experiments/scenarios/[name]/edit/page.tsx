import { decodeSegment } from "@/lib/routes"
import { ScenarioEditorScreen } from "@/screens/experiments/scenario-editor"

type Props = { params: Promise<{ name: string }> }

export async function generateMetadata({ params }: Props) {
  const { name } = await params
  return { title: `${decodeSegment(name)} 수정` }
}

export default async function Page({ params }: Props) {
  const { name } = await params
  return <ScenarioEditorScreen name={decodeSegment(name)} />
}

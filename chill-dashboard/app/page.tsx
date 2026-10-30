import { Suspense } from "react"
import { LiveBoardPage } from "@/features/live-board"

export default function Page() {
  return <Suspense fallback={<p className="screen" role="status">라이브 보드 여는 중</p>}><LiveBoardPage /></Suspense>
}

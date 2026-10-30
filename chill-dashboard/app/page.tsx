import { Suspense } from "react"
import { OverviewPage } from "@/features/overview"

export default function Page() {
  return <Suspense fallback={<p className="screen" role="status">개요 여는 중</p>}><OverviewPage /></Suspense>
}

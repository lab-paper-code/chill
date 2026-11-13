"use client"

import { useRouter } from "next/navigation"
import { ErrorSection } from "@/components/custom/blocks/ErrorBlock"

// Error boundaries must be Client Components; `retry` re-renders the segment.
export default function Error({ error, retry }: { error: Error & { digest?: string }; retry: () => void }) {
  const router = useRouter()
  return <ErrorSection errorCode="오류" title="화면을 표시하지 못함" errorId={error.digest} onRetry={retry} onHome={() => router.push("/")} />
}

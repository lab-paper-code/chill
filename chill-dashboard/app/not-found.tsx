import { NotFoundSection } from "@/components/custom/blocks/NotFoundBlock"

export const metadata = { title: "페이지 없음" }

export default function NotFound() {
  return (
    <main id="main" className="bg-background">
      <NotFoundSection code="404" title="페이지 없음" primaryAction={{ label: "개요로", href: "/" }} />
    </main>
  )
}

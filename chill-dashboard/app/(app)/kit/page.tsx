import { notFound } from "next/navigation"
import { KitShowcase } from "./showcase"

export const metadata = { title: "부품" }

/** Development-only gallery of the shared kit with sample CHILL data. */
export default function Page() {
  if (process.env.NODE_ENV === "production") notFound()
  return <KitShowcase />
}

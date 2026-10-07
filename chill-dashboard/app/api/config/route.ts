import { publicConfig } from "@/lib/server-config"
import { relayFailure } from "@/lib/relay"

export const runtime = "nodejs"
export const dynamic = "force-dynamic"

export function GET() {
  try {
    return Response.json(publicConfig(), { headers: { "Cache-Control": "no-store" } })
  } catch {
    return relayFailure("DashboardConfigInvalid", 503)
  }
}

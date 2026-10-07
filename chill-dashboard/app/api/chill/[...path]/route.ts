import { relay } from "@/lib/relay"

export const runtime = "nodejs"
export const dynamic = "force-dynamic"

async function handle(request: Request, context: { params: Promise<{ path: string[] }> }) {
  const { path } = await context.params
  return relay(request, path)
}

export { handle as GET, handle as POST, handle as PUT, handle as DELETE, handle as PATCH, handle as HEAD, handle as OPTIONS }

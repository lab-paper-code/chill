export type PublicConfig = {
  namespace: string
  systemName: string
  queryTimeoutMs: number
  mutationTimeoutMs: number
  inferTimeoutMs: number
  maxRequestBytes: number
  pageSize: number
  maxPageSize: number
  maxWindowHours: number
  pollIntervalMs: number
}

export type ServerConfig = PublicConfig & { chillServer: URL }

// Evaluated at request time: standalone images receive deployment settings at runtime.
export function serverConfig(env: Readonly<Record<string, string | undefined>> = process.env): ServerConfig {
  const invalid = () => { throw new Error("DashboardConfigInvalid") }
  const integer = (key: string, fallback: number, maximum: number) => {
    const raw = env[key]
    if (raw === undefined) return fallback
    if (!/^[1-9]\d*$/.test(raw)) return invalid()
    const value = Number(raw)
    if (!Number.isSafeInteger(value) || value > maximum) return invalid()
    return value
  }
  const name = (key: string, maximum: number) => {
    const value = env[key]
    if (!value || value.length > maximum || !/^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/.test(value) || value.split(".").some(part => !part || part.startsWith("-") || part.endsWith("-"))) return invalid()
    return value
  }
  const raw = env.CHILL_BASE_URL
  if (!raw || raw !== raw.trim() || !/^https:\/\/[^/?#\\@]+\/?$/.test(raw)) return invalid()
  let chillServer: URL
  try { chillServer = new URL(raw) } catch { return invalid() }
  if (chillServer.protocol !== "https:" || !chillServer.hostname || chillServer.username || chillServer.password || chillServer.pathname !== "/" || chillServer.search || chillServer.hash || raw.includes("\\")) return invalid()
  const config = {
    chillServer,
    namespace: name("CHILL_NAMESPACE", 63),
    systemName: name("CHILL_SYSTEM_NAME", 253),
    queryTimeoutMs: integer("CHILL_QUERY_TIMEOUT_MS", 10_000, 3_600_000),
    mutationTimeoutMs: integer("CHILL_MUTATION_TIMEOUT_MS", 60_000, 3_600_000),
    inferTimeoutMs: integer("CHILL_INFER_TIMEOUT_MS", 30_000, 3_600_000),
    maxRequestBytes: integer("CHILL_MAX_REQUEST_BYTES", 8_388_608, 67_108_864),
    pageSize: integer("CHILL_PAGE_SIZE", 50, 200),
    maxPageSize: integer("CHILL_MAX_PAGE_SIZE", 200, 200),
    maxWindowHours: integer("CHILL_MAX_WINDOW_HOURS", 24, 24 * 365),
    pollIntervalMs: integer("CHILL_POLL_INTERVAL_MS", 5_000, 3_600_000),
  }
  if (config.pageSize > config.maxPageSize || config.pollIntervalMs < 1_000 || config.namespace.includes(".")) return invalid()
  return config
}

export function publicConfig(): PublicConfig {
  const config = serverConfig()
  return {
    namespace: config.namespace,
    systemName: config.systemName,
    queryTimeoutMs: config.queryTimeoutMs,
    mutationTimeoutMs: config.mutationTimeoutMs,
    inferTimeoutMs: config.inferTimeoutMs,
    maxRequestBytes: config.maxRequestBytes,
    pageSize: config.pageSize,
    maxPageSize: config.maxPageSize,
    maxWindowHours: config.maxWindowHours,
    pollIntervalMs: config.pollIntervalMs,
  }
}

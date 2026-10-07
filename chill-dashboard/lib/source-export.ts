// Preserve source identity, timestamps, conditions and units; never download credentials.
export function safeSource(value:unknown):unknown {
  if (Array.isArray(value)) return value.map(safeSource)
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key,item]) => [key,/(authorization|cookie|token|password|credential|privatekey|accesskey|secretkey|signedurl|presigned)/i.test(key) ? "[비밀정보 제거]" : safeSource(item)]))
  }
  if (typeof value === "string") {
    if (/\bBearer\s+\S+/i.test(value)) return "[인증정보 제거]"
    try {
      const url = new URL(value)
      if (url.username || url.password || [...url.searchParams.keys()].some(key => /token|secret|signature|credential|password|key/i.test(key))) return "[credential URL 제거]"
    } catch { /* ordinary source text is not a URL */ }
  }
  return value
}

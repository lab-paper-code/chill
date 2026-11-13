// URL structure from design section 3: sidebar groups, section tab rows and breadcrumb labels.

export type NavItem = { href: string; label: string; icon: "overview" | "services" | "devices" | "scheduler" | "measurements" | "experiments" | "catalog" }
export const navigation: { label: string; items: NavItem[] }[] = [
  { label: "운영", items: [
    { href: "/", label: "개요", icon: "overview" },
    { href: "/services", label: "서비스", icon: "services" },
    { href: "/devices", label: "장치", icon: "devices" },
    { href: "/scheduler", label: "스케줄러", icon: "scheduler" },
  ] },
  { label: "분석", items: [
    { href: "/measurements", label: "측정", icon: "measurements" },
    { href: "/experiments", label: "실험", icon: "experiments" },
  ] },
  { label: "관리", items: [
    { href: "/catalog", label: "카탈로그", icon: "catalog" },
  ] },
]

/** Active sidebar item: the longest prefix match ("/" only matches itself). */
export function activeNav(pathname: string): string | undefined {
  return navigation.flatMap(group => group.items).map(item => item.href)
    .filter(href => href === "/" ? pathname === "/" : pathname === href || pathname.startsWith(`${href}/`))
    .sort((a, b) => b.length - a.length)[0]
}

export type TabLink = { href: string; label: string }
/** Shared tab rows of the section pages. Screens pass these to `PageTabs`. */
export const sectionTabs = {
  scheduler: [
    { href: "/scheduler/decisions", label: "판단" },
    { href: "/scheduler/actions", label: "작업" },
    { href: "/scheduler/settings", label: "설정" },
  ],
  measurements: [
    { href: "/measurements/coverage", label: "범위" },
    { href: "/measurements/requests", label: "요청" },
    { href: "/measurements/results", label: "결과" },
  ],
  experiments: [
    { href: "/experiments/runs", label: "실행" },
    { href: "/experiments/results", label: "결과" },
    { href: "/experiments/scenarios", label: "부하 시나리오" },
  ],
  catalog: [
    { href: "/catalog/models", label: "모델" },
    { href: "/catalog/preprocessors", label: "전처리기" },
    { href: "/catalog/datasets", label: "데이터셋" },
  ],
} satisfies Record<string, TabLink[]>

export const catalogKinds = { models: "모델", preprocessors: "전처리기", datasets: "데이터셋" } as const
export type CatalogKind = keyof typeof catalogKinds
export const isCatalogKind = (value: string): value is CatalogKind => Object.hasOwn(catalogKinds, value)

const segmentLabels: Record<string, string> = {
  services: "서비스", devices: "장치", scheduler: "스케줄러", measurements: "측정", experiments: "실험", catalog: "카탈로그",
  decisions: "판단", actions: "작업", settings: "설정", coverage: "범위", requests: "요청", runs: "실행", results: "결과",
  scenarios: "부하 시나리오", ...catalogKinds, new: "새로 만들기", edit: "수정", kit: "부품",
}

/** `dynamic` marks a route parameter (a name or an id); the page may replace its label with its title. */
export type Crumb = { label: string; href: string; dynamic?: boolean }
// UUIDs and long hex digests are never shown as a location.
const idLike = (text: string) => /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(text) || /^(sha256:)?[0-9a-f]{16,}$/i.test(text)
/** Route params arrive percent-encoded; malformed escapes stay as written. */
export function decodeSegment(segment: string): string {
  try { return decodeURIComponent(segment) } catch { return segment }
}

/**
 * Breadcrumb trail for detail, new and edit pages; empty for list pages, which show only their title.
 * A path is a list page when it is a sidebar item or a section tab.
 */
export function breadcrumbs(pathname: string): Crumb[] {
  const tabs = Object.values(sectionTabs).flat().map(tab => tab.href)
  const lists = new Set([...navigation.flatMap(group => group.items.map(item => item.href)), ...tabs, "/kit"])
  if (lists.has(pathname)) return []
  const parts = pathname.split("/").filter(Boolean)
  const trail: Crumb[] = []
  parts.forEach((part, index) => {
    const href = `/${parts.slice(0, index + 1).join("/")}`
    // `/measurements/runs/x` has no list page of its own; the run belongs to the request list.
    if (href === "/measurements/runs") return
    // Only route words get a label; names (a service called "new") stay as written.
    const structural = index === 0 || (index === 1 && parts[0] in sectionTabs) || (index === parts.length - 1 && (part === "new" || part === "edit"))
    if (structural) trail.push({ label: segmentLabels[part] ?? decodeSegment(part), href })
    else trail.push({ label: idLike(decodeSegment(part)) ? "상세" : decodeSegment(part), href, dynamic: true })
  })
  return trail
}

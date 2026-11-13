"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useRouter } from "next/navigation"
import { Boxes, ChevronsLeft, Cpu, FlaskConical, Gauge, Layers, LayoutDashboard, Pause, Play, RefreshCw, Ruler, Search, Snowflake, Workflow } from "lucide-react"
import { cn } from "@/lib/utils"
import { SessionProvider, useChillQuery, useInitializing, useSession, useSignOut } from "@/lib/client"
import { useCoverageInventory } from "@/hooks/use-measurement-data"
import { parseSchedulerJSON } from "@/lib/scheduler"
import { runHref, type Run } from "@/lib/experiments-run"
import { decodeExperimentPage, type ExperimentPage } from "@/lib/experiments"
import { resultName } from "@/lib/experiments-results"
import type { LoadScenario } from "@/lib/evaluation-author"
import { LiveProvider, useLive } from "@/lib/use-live"
import { activeNav, breadcrumbs, navigation, sectionTabs, type NavItem } from "@/lib/routes"
import type { WorkloadSummary, ModelList } from "@/lib/domain"
import type { ActiveEvaluations } from "@/lib/evaluation-active"
import type { StatusInfo } from "@/lib/status-labels"
import { MobileSidebarTrigger, Sidebar, SidebarBrand, SidebarContent, SidebarFooter, SidebarGroup, SidebarHeader, SidebarItem, SidebarProvider } from "@/components/custom/blocks/SidebarBlock"
import { useSidebar } from "@/components/custom/blocks/sidebar-context"
import { TooltipProvider } from "@/components/custom/blocks/TooltipBlock"
import { ToastProvider } from "@/components/custom/blocks/ToastBlock"
import { BreadcrumbBlock } from "@/components/custom/blocks/BreadcrumbBlock"
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, useCommandSearch } from "@/components/custom/blocks/CommandBlock"
import { Kbd } from "@/components/custom/blocks/KbdBlock"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/custom/blocks/PopoverBlock"
import { ThemeSwitcher } from "@/components/custom/blocks/ThemeSwitcherBlock"
import Button from "@/components/custom/blocks/Button"
import { koreanValidity } from "@/components/custom/blocks/validity"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { Fields, StatusDot, TimeText } from "@/components/kit/display"
import { usePageTitle } from "@/components/kit/page"
import { SignIn } from "./sign-in"

const icons: Record<NavItem["icon"], React.ElementType> = { overview: LayoutDashboard, services: Layers, devices: Cpu, scheduler: Workflow, measurements: Ruler, experiments: FlaskConical, catalog: Boxes }

/** Providers, sidebar, top bar and the content column for every signed-in page. */
export function AppShell({ children }: { children: React.ReactNode }) {
  // Native validation bubbles of controls outside the kit say Korean text (kit controls show their own message).
  React.useEffect(koreanValidity, [])
  return (
    <SessionProvider signIn={SignIn}>
      <React.Suspense fallback={null}>
        <LiveProvider>
          <TooltipProvider>
            <ToastProvider position="bottom-right">
              <SidebarProvider>
                <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-[60] focus:rounded-md focus:bg-card focus:px-3 focus:py-2 focus:text-sm">본문으로 건너뛰기</a>
                <AppSidebar />
                <div className="flex min-w-0 flex-1 flex-col">
                  <TopBar />
                  <main id="main" tabIndex={-1} className="mx-auto w-full min-w-0 max-w-[1440px] flex-1 px-4 py-6 outline-none sm:px-6">{children}</main>
                </div>
              </SidebarProvider>
            </ToastProvider>
          </TooltipProvider>
        </LiveProvider>
      </React.Suspense>
    </SessionProvider>
  )
}

function AppSidebar() {
  const pathname = usePathname()
  const active = activeNav(pathname)
  const { expanded, setExpanded } = useSidebar()
  return (
    <Sidebar>
      <SidebarHeader>
        <SidebarBrand icon={<Snowflake className="size-5 text-primary" aria-hidden="true" />}>CHILL</SidebarBrand>
      </SidebarHeader>
      <SidebarContent>
        <nav aria-label="주 메뉴" className="space-y-5">
          {navigation.map(group => (
            <SidebarGroup key={group.label} label={group.label}>
              {group.items.map(item => {
                const Icon = icons[item.icon]
                return <SidebarItem key={item.href} href={item.href} linkComponent={Link} active={active === item.href} icon={<Icon />}>{item.label}</SidebarItem>
              })}
            </SidebarGroup>
          ))}
        </nav>
      </SidebarContent>
      <SidebarFooter className="hidden md:flex">
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          aria-label={expanded ? "메뉴 접기" : "메뉴 펼치기"}
          aria-expanded={expanded}
          className={cn("flex h-9 w-full items-center gap-3 rounded-md px-3 text-sm text-muted-foreground hover:bg-muted hover:text-foreground", !expanded && "justify-center px-0")}
        >
          <ChevronsLeft className={cn("size-[18px] shrink-0 transition-transform", !expanded && "rotate-180")} aria-hidden="true" />
          {expanded && <span>접기</span>}
        </button>
      </SidebarFooter>
    </Sidebar>
  )
}

function TopBar() {
  const pathname = usePathname()
  const page = usePageTitle(pathname)
  // The last crumb of a detail or new page is a name, an id or "새로 만들기": the PageHeader title says it better.
  // An edit page keeps "수정" (the crumb before it already names the object). Parents the URL lacks go before the last crumb.
  const crumbs = breadcrumbs(pathname).map((crumb, index, all) => index === all.length - 1 && crumb.titled && page && !crumb.href.endsWith("/edit") ? { ...crumb, label: page.title } : crumb)
  if (page?.parents.length && crumbs.length) crumbs.splice(crumbs.length - 1, 0, ...page.parents.filter(parent => !crumbs.some(crumb => crumb.href === parent.href)))
  const [paletteOpen, setPaletteOpen] = React.useState(false)
  React.useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault()
        setPaletteOpen(open => !open)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
  return (
    <header className="sticky top-0 z-30 flex h-14 shrink-0 items-center gap-2 border-b border-border bg-background/95 px-3 backdrop-blur-sm sm:px-6">
      <MobileSidebarTrigger />
      <div className="min-w-0 flex-1">
        {crumbs.length > 0 && <BreadcrumbBlock items={crumbs} linkComponent={Link} color="primary" maxItems={4} />}
      </div>
      <Activity />
      <LiveStatus />
      <button
        type="button"
        onClick={() => setPaletteOpen(true)}
        aria-keyshortcuts="Control+K"
        className="inline-flex h-8 items-center gap-2 rounded-md border border-border bg-card px-2 text-sm text-muted-foreground hover:bg-muted sm:w-48 sm:px-2.5"
      >
        <Search className="size-4" aria-hidden="true" />
        <span className="hidden flex-1 text-left sm:inline">검색</span>
        <span className="sr-only sm:hidden">검색</span>
        <span className="hidden items-center gap-0.5 sm:inline-flex"><Kbd>Ctrl</Kbd><Kbd>K</Kbd></span>
      </button>
      <ThemeSwitcher variant="icon" reveal={false} />
      <AccountMenu />
      <Palette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </header>
  )
}

/** Running evaluations and devices under measurement; hidden while there are none. */
function Activity() {
  const active = useChillQuery<ActiveEvaluations>("v1/evaluations/active", { poll: true })
  const { state } = useLive()
  const running = active.data?.items.length ?? 0
  const measuring = state.snapshot?.nodes.filter(node => node.measurement).length ?? 0
  const chip = "inline-flex h-8 items-center gap-1.5 rounded-md bg-primary-soft px-2 text-xs font-medium text-primary hover:bg-primary/15"
  // Phones keep the icon and the count; the name stays in the accessible label.
  return (
    <div className="flex items-center gap-1.5">
      {running > 0 && <Link href="/experiments/runs" aria-label={`실험 실행 중 ${running}`} className={chip}><FlaskConical className="size-3.5" aria-hidden="true" /><span className="hidden md:inline">실험 실행 중</span> <span className="tabular-nums">{running}</span></Link>}
      {measuring > 0 && <Link href="/devices" aria-label={`측정 중 장치 ${measuring}`} className={chip}><Gauge className="size-3.5" aria-hidden="true" /><span className="hidden md:inline">측정 중 장치</span> <span className="tabular-nums">{measuring}</span></Link>}
    </div>
  )
}

const connection: Record<string, StatusInfo> = {
  live: { label: "실시간", tone: "ok" },
  fixture: { label: "재생", tone: "ok" },
  idle: { label: "연결 중", tone: "progress" },
  connecting: { label: "연결 중", tone: "progress" },
  resuming: { label: "동기화 중", tone: "progress" },
  reconnecting: { label: "재연결 중", tone: "warn" },
  "paused-hidden": { label: "대기", tone: "idle" },
  "protocol-error": { label: "형식 오류", tone: "fail" },
  unauthorized: { label: "인증 필요", tone: "fail" },
  forbidden: { label: "권한 없음", tone: "fail" },
}

/** Live connection state; the popover holds pause, reconnect and the server rows. */
function LiveStatus() {
  const live = useLive()
  const initializing = useInitializing()
  const server = useChillQuery<ActiveEvaluations>("v1/evaluations/active", { poll: true })
  const state = connection[live.status] ?? { label: live.status, tone: "idle" as const }
  const shown: StatusInfo = live.paused ? { label: "일시정지", tone: "idle" } : state
  const serverState: StatusInfo = server.error ? ([401, 403].includes(server.error.status) ? { label: "권한 없음", tone: "warn" } : { label: "실패", tone: "fail" }) : server.data ? { label: "정상", tone: "ok" } : { label: "확인 중", tone: "progress" }
  const disconnected = !["live", "fixture", "connecting", "resuming", "idle"].includes(live.status)
  return (
    <Popover>
      <PopoverTrigger className="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-sm hover:bg-muted" aria-label={`실시간 상태: ${shown.label}`}>
        <StatusDot status={shown} />
        <span className="hidden lg:inline">{shown.label}</span>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-72 p-4 text-sm lg:p-4 lg:text-sm">
        <h2 className="mb-3 text-sm font-semibold">실시간 상태</h2>
        <Fields columns={1} items={[
          { label: "연결", value: <StatusDot status={state} showLabel /> },
          { label: "마지막 수신", value: live.lastReceivedAt ? <TimeText at={live.lastReceivedAt} mode="relative" /> : null, reason: "수신 없음" },
          { label: "서버 연결", value: <StatusDot status={serverState} showLabel /> },
          { label: "서버 초기화", value: <StatusDot status={initializing ? { label: "초기화 중", tone: "progress" } : { label: "완료", tone: "ok" }} showLabel /> },
        ]} />
        <div className="mt-4 flex gap-2">
          {live.paused
            ? <Button size="sm" variant="default" iconLeft={<Play />} onClick={live.resume}>다시 시작</Button>
            : <Button size="sm" variant="outline" iconLeft={<Pause />} onClick={live.pause}>일시정지</Button>}
          {disconnected && <Button size="sm" variant="outline" iconLeft={<RefreshCw />} onClick={live.reconnect}>다시 연결</Button>}
        </div>
      </PopoverContent>
    </Popover>
  )
}

function AccountMenu() {
  const { config } = useSession()
  const signOut = useSignOut()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="inline-flex h-8 max-w-40 items-center gap-1.5 rounded-md px-2 text-sm hover:bg-muted" aria-label={`계정: ${config.systemName}`}>
        <span className="grid size-6 shrink-0 place-items-center rounded-full bg-muted text-xs font-semibold uppercase" aria-hidden="true">{config.systemName.slice(0, 1)}</span>
        <span className="hidden truncate lg:inline">{config.systemName}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuLabel className="grid grid-cols-[5rem_1fr] gap-y-1 font-normal">
          <span className="text-muted-foreground">접속 대상</span><span className="truncate">{config.systemName}</span>
          <span className="text-muted-foreground">네임스페이스</span><span className="truncate">{config.namespace}</span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onSelect={signOut}>이 탭에서 토큰 지우기</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** Ctrl K: jump to a page, service, device or model by name. */
function Palette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const router = useRouter()
  const go = (href: string) => { onOpenChange(false); router.push(href) }
  return (
    <CommandDialog open={open} onOpenChange={onOpenChange} aria-label="검색">
      <CommandInput placeholder="화면, 서비스, 장치, 카탈로그, 실험" aria-label="검색어" />
      <CommandList className="max-h-[min(60dvh,420px)]">
        <CommandEmpty>결과 없음</CommandEmpty>
        <PaletteGroups go={go} />
      </CommandList>
    </CommandDialog>
  )
}

type Named = { metadata: { name: string; uid: string } }
type PaletteEntry = { key: string; label: string; kind?: string; href: string }

/**
 * Mounted only while the palette is open, so its lists load lazily. Every list uses the path and decoder
 * of the screen that shows it, so the cache is shared and nothing is fetched twice.
 */
function PaletteGroups({ go }: { go: (href: string) => void }) {
  const search = useCommandSearch().trim()
  const workloads = useChillQuery<WorkloadSummary[]>("v1/workloads?view=summary")
  const inventory = useCoverageInventory()
  const models = useChillQuery<ModelList>("v1/models")
  const preprocessors = useChillQuery<{ items: Named[] }>("v1/preprocessors")
  const datasets = useChillQuery<{ items: Named[] }>("v1/datasets")
  const runs = useChillQuery<{ items: Run[] }>("v1/evaluations", { decoder: parseSchedulerJSON })
  const scenarios = useChillQuery<{ items: LoadScenario[] }>("v1/evaluations/load-scenarios", { decoder: parseSchedulerJSON })
  // The newest 50 published results (the server's page limit).
  const results = useChillQuery<ExperimentPage>("v1/experiments?limit=50&offset=0", { decoder: decodeExperimentPage })
  const areaOf = (href: string) => navigation.flatMap(group => group.items).find(item => href.startsWith(`${item.href}/`))?.label ?? ""
  const catalog = (kind: string, word: string, items: Named[] | undefined): PaletteEntry[] =>
    (items ?? []).map(item => ({ key: `${kind}-${item.metadata.uid}`, label: item.metadata.name, kind: word, href: `/catalog/${kind}/${encodeURIComponent(item.metadata.name)}?uid=${encodeURIComponent(item.metadata.uid)}` }))
  const groups: { heading: string; entries: PaletteEntry[] }[] = [
    { heading: "화면", entries: [
      ...navigation.flatMap(group => group.items.map(item => ({ key: item.href, label: item.label, href: item.href }))),
      ...Object.values(sectionTabs).flatMap(tabs => tabs.map(tab => ({ key: tab.href, label: `${areaOf(tab.href)} ${tab.label}`, href: tab.href }))),
    ] },
    { heading: "서비스", entries: (Array.isArray(workloads.data) ? workloads.data : []).map(row => ({ key: row.desired.metadata.uid, label: row.desired.metadata.name, href: `/services/${encodeURIComponent(row.desired.metadata.name)}?uid=${encodeURIComponent(row.desired.metadata.uid)}` })) },
    // Inventory, not the live stream: excluded and idle devices are found too.
    { heading: "장치", entries: [...new Map((inventory.data?.classes ?? []).flatMap(cls => cls.nodes.map(node => [node.uid, { key: node.uid, label: node.name, href: `/devices/${encodeURIComponent(node.name)}` }]))).values()] },
    { heading: "실험", entries: [
      ...(runs.data?.items ?? []).map(run => ({ key: run.uid, label: run.name, kind: "실행", href: runHref(run) })),
      ...(results.data?.items ?? []).map(result => ({ key: result.id, label: resultName(result), kind: "결과", href: `/experiments/results/${encodeURIComponent(result.id)}` })),
      ...(scenarios.data?.items ?? []).map(scenario => ({ key: scenario.uid, label: scenario.name, kind: "부하 시나리오", href: `/experiments/scenarios/${encodeURIComponent(scenario.name)}` })),
    ] },
    { heading: "카탈로그", entries: [...catalog("models", "모델", models.data?.items), ...catalog("preprocessors", "전처리기", preprocessors.data?.items), ...catalog("datasets", "데이터셋", datasets.data?.items)] },
  ]
  const item = "gap-2 px-2.5 py-2"
  return (
    <>
      {groups.map(group => (
        <CommandGroup key={group.heading} heading={group.heading}>
          {group.entries.map(entry => (
            <CommandItem key={entry.key} className={item} value={`${group.heading} ${entry.kind ?? ""} ${entry.label}`} onSelect={() => go(entry.href)}>
              <span className="min-w-0 flex-1 truncate">{entry.label}</span>
              {entry.kind && <span className="shrink-0 text-xs text-muted-foreground">{entry.kind}</span>}
            </CommandItem>
          ))}
        </CommandGroup>
      ))}
      {search && (
        <CommandGroup heading="측정">
          <CommandItem className={item} value={`측정 요청 ${search}`} onSelect={() => go(`/measurements/requests?${new URLSearchParams({ q: search })}`)}>
            <Search className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />측정 요청에서 &lsquo;{search}&rsquo; 찾기
          </CommandItem>
        </CommandGroup>
      )}
    </>
  )
}

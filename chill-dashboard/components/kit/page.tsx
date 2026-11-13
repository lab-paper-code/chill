"use client"

import * as React from "react"
import Link from "next/link"
import { usePathname, useSearchParams } from "next/navigation"
import { MoreHorizontal } from "lucide-react"
import { cn } from "@/lib/utils"
import type { StatusInfo } from "@/lib/status-labels"
import type { TabLink } from "@/lib/routes"
import { buttonClassName } from "@/components/custom/blocks/Button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { StatusBadge } from "./display"

export type MoreAction = {
  label: string; onSelect: () => void; destructive?: boolean; disabled?: boolean
  /** Why the action is unavailable. The item stays focusable and shows the reason. */ disabledReason?: string
}

/** Overflow menu button; destructive items are listed last, after a separator. */
export function MoreMenu({ items, triggerClassName, ...aria }: { items: MoreAction[]; triggerClassName?: string; "aria-label"?: string; "aria-labelledby"?: string }) {
  const safe = items.filter(item => !item.destructive)
  const risky = items.filter(item => item.destructive)
  const entry = (item: MoreAction) => {
    const blocked = !!item.disabled && !!item.disabledReason
    return (
      <DropdownMenuItem
        key={item.label}
        variant={item.destructive ? "destructive" : "default"}
        disabled={!!item.disabled && !blocked}
        aria-disabled={blocked || undefined}
        className={cn(blocked && "opacity-60")}
        onSelect={event => { if (blocked) event.preventDefault(); else item.onSelect() }}
      >
        {item.label}
        {blocked && <span className="ml-auto pl-4 text-xs text-muted-foreground">{item.disabledReason}</span>}
      </DropdownMenuItem>
    )
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className={cn(buttonClassName({ variant: "outline", size: "icon" }), triggerClassName)} aria-label={aria["aria-labelledby"] ? undefined : aria["aria-label"] ?? "더보기"} aria-labelledby={aria["aria-labelledby"]}>
        <MoreHorizontal aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-40">
        {safe.map(entry)}
        {safe.length > 0 && risky.length > 0 && <DropdownMenuSeparator />}
        {risky.map(entry)}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

export type ParentCrumb = { label: string; href: string }
type PageEntry = { path: string; title: string; parents: ParentCrumb[] }
// The current page title (and parents the URL does not show), read by the top bar for the breadcrumbs.
let pageTitle: PageEntry | null = null
const titleListeners = new Set<() => void>()
const setPageTitle = (next: typeof pageTitle) => { pageTitle = next; titleListeners.forEach(notify => notify()) }
const subscribeTitle = (listener: () => void) => { titleListeners.add(listener); return () => { titleListeners.delete(listener) } }
/** Title and parent crumbs registered by the PageHeader of `pathname`, if it is mounted. */
export function usePageTitle(pathname: string): PageEntry | undefined {
  const current = React.useSyncExternalStore(subscribeTitle, () => pageTitle, () => null)
  return current?.path === pathname ? current : undefined
}

/** Page title, status badges, one primary action, and the overflow menu for the rest. Long titles wrap. */
export function PageHeader({ title, status, badges, actions, more, parents }: {
  title: string
  status?: StatusInfo
  /** Extra badges after the status, e.g. admission. */
  badges?: React.ReactNode
  /** The one primary action (and at most one secondary). */
  actions?: React.ReactNode
  /** Overflow menu items. Destructive items are listed last, after a separator. */
  more?: MoreAction[]
  /** Breadcrumbs the URL does not hold, inserted before the page's own crumb (e.g. a run's parent request). */
  parents?: ParentCrumb[]
}) {
  const pathname = usePathname()
  const parentsKey = JSON.stringify(parents ?? [])
  React.useEffect(() => {
    const entry = { path: pathname, title, parents: JSON.parse(parentsKey) as ParentCrumb[] }
    setPageTitle(entry)
    return () => { if (pageTitle === entry) setPageTitle(null) }
  }, [pathname, title, parentsKey])
  return (
    <header className="flex flex-wrap items-start gap-x-3 gap-y-2">
      <div className="flex min-h-9 min-w-0 flex-[1_1_16rem] flex-wrap items-center gap-x-3 gap-y-1.5">
        <h1 id="page-title" tabIndex={-1} className="min-w-0 text-xl font-semibold tracking-tight text-foreground outline-none [overflow-wrap:anywhere]">{title}</h1>
        {(status || badges) && <div className="flex flex-wrap items-center gap-2">{status && <StatusBadge status={status} />}{badges}</div>}
      </div>
      {(actions || (more && more.length > 0)) && (
        <div className="ml-auto flex shrink-0 flex-wrap items-center justify-end gap-2">
          {actions}
          {more && more.length > 0 && <MoreMenu items={more} />}
        </div>
      )}
    </header>
  )
}

type QueryTab = { value: string; label: string; count?: number }
type PageTabsProps =
  | { tabs: TabLink[]; label?: string }
  | { tabs: QueryTab[]; param: string; defaultValue: string; label?: string }

/**
 * One row of tabs under the header. Link tabs change the route (section pages);
 * with `param` they set `?<param>=` and keep the other query values.
 * Both are navigation: links with aria-current, not an ARIA tablist. On phones the row scrolls.
 */
export function PageTabs(props: PageTabsProps) {
  const pathname = usePathname()
  const search = useSearchParams()
  const items = "param" in props
    ? props.tabs.map(tab => {
        const next = new URLSearchParams(search.toString())
        if (tab.value === props.defaultValue) next.delete(props.param)
        else next.set(props.param, tab.value)
        const query = next.toString()
        return { key: tab.value, href: `${pathname}${query ? `?${query}` : ""}`, label: tab.label, count: tab.count, active: (search.get(props.param) ?? props.defaultValue) === tab.value }
      })
    : props.tabs.map(tab => ({ key: tab.href, href: tab.href, label: tab.label, count: undefined, active: pathname === tab.href || pathname.startsWith(`${tab.href}/`) }))
  return (
    <nav aria-label={props.label ?? "하위 화면"} className="-mx-1 overflow-x-auto px-1 [scrollbar-width:none]">
      <ul className="flex min-w-max items-center gap-1 border-b border-border">
        {items.map(item => (
          <li key={item.key}>
            <Link
              href={item.href}
              scroll={false}
              aria-current={item.active ? "page" : undefined}
              className={cn(
                "relative inline-flex h-9 items-center gap-1.5 px-3 text-sm font-medium outline-offset-[-2px] transition-colors",
                "after:absolute after:inset-x-2 after:-bottom-px after:border-b-2",
                item.active ? "text-foreground after:border-primary" : "text-muted-foreground after:border-transparent hover:text-foreground",
              )}
            >
              {item.label}
              {item.count != null && <span className="rounded bg-muted px-1.5 text-xs tabular-nums text-muted-foreground">{item.count}</span>}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  )
}

export type RailItem = { value: string; label: string; count?: number; /** Link item; without it the item is a button that calls `onChange`. */ href?: string }
/**
 * Vertical group navigation beside its content on desktops; a horizontal scroll strip above it on phones.
 * With `children` it lays out the rail and the content side by side.
 */
export function Rail({ label, items, value, onChange, children }: { label: string; items: RailItem[]; value: string; onChange?: (value: string) => void; children?: React.ReactNode }) {
  const nav = (
    <nav aria-label={label} className="min-w-0 lg:sticky lg:top-20 lg:self-start">
      <ul className="-mx-1 flex gap-1 overflow-x-auto px-1 [scrollbar-width:none] lg:mx-0 lg:flex-col lg:overflow-visible lg:px-0">
        {items.map(item => {
          const active = item.value === value
          const className = cn(
            "flex h-9 w-full items-center justify-between gap-2 whitespace-nowrap rounded-md px-3 text-sm outline-offset-[-2px] transition-colors",
            active ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
          )
          const body = <>{item.label}{item.count != null && <span className="rounded bg-primary-soft px-1.5 text-xs tabular-nums text-primary">{item.count}</span>}</>
          return (
            <li key={item.value} className="shrink-0">
              {item.href
                ? <Link href={item.href} scroll={false} aria-current={active ? "page" : undefined} className={className}>{body}</Link>
                : <button type="button" aria-current={active || undefined} onClick={() => onChange?.(item.value)} className={className}>{body}</button>}
            </li>
          )
        })}
      </ul>
    </nav>
  )
  if (children === undefined) return nav
  return <div className="grid gap-4 lg:grid-cols-[11rem_minmax(0,1fr)] lg:gap-6">{nav}<div className="min-w-0">{children}</div></div>
}

/** Vertical rhythm of a screen: header, optional tabs, then sections 24px apart. */
export function Page({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn("flex flex-col gap-6", className)}>{children}</div>
}

/** A titled region of a page: an h2 and its content, on a card when `card` is set. */
export function Section({ title, actions, card = false, children, className }: { title?: string; actions?: React.ReactNode; card?: boolean; children: React.ReactNode; className?: string }) {
  const id = React.useId()
  return (
    <section aria-labelledby={title ? id : undefined} className={cn("min-w-0", card && "rounded-xl border border-border bg-card p-4", className)}>
      {(title || actions) && (
        <div className="mb-3 flex min-h-8 flex-wrap items-center gap-x-3 gap-y-2">
          {title && <h2 id={id} className="min-w-0 text-sm font-semibold text-foreground">{title}</h2>}
          {actions && <div className="ml-auto flex flex-wrap items-center gap-2">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  )
}

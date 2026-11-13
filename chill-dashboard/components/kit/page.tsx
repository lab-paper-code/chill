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

export type MoreAction = { label: string; onSelect: () => void; destructive?: boolean; disabled?: boolean }

/** Page title, status badges, one primary action, and the overflow menu for the rest. */
export function PageHeader({ title, status, badges, actions, more }: {
  title: string
  status?: StatusInfo
  /** Extra badges after the status, e.g. admission. */
  badges?: React.ReactNode
  /** The one primary action (and at most one secondary). */
  actions?: React.ReactNode
  /** Overflow menu items. Destructive items are listed last, after a separator. */
  more?: MoreAction[]
}) {
  const safe = more?.filter(item => !item.destructive) ?? []
  const risky = more?.filter(item => item.destructive) ?? []
  return (
    <header className="flex min-h-9 flex-wrap items-center gap-x-3 gap-y-2">
      <h1 id="page-title" tabIndex={-1} className="min-w-0 truncate text-xl font-semibold tracking-tight text-foreground outline-none">{title}</h1>
      {(status || badges) && <div className="flex items-center gap-2">{status && <StatusBadge status={status} />}{badges}</div>}
      <div className="ml-auto flex items-center gap-2">
        {actions}
        {more && more.length > 0 && (
          <DropdownMenu>
            <DropdownMenuTrigger className={buttonClassName({ variant: "outline", size: "icon" })} aria-label="더보기">
              <MoreHorizontal aria-hidden="true" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-40">
              {safe.map(item => <DropdownMenuItem key={item.label} disabled={item.disabled} onSelect={item.onSelect}>{item.label}</DropdownMenuItem>)}
              {safe.length > 0 && risky.length > 0 && <DropdownMenuSeparator />}
              {risky.map(item => <DropdownMenuItem key={item.label} variant="destructive" disabled={item.disabled} onSelect={item.onSelect}>{item.label}</DropdownMenuItem>)}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
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
 * Both are navigation: links with aria-current, not an ARIA tablist.
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
                "relative inline-flex h-9 items-center gap-1.5 px-3 text-sm font-medium transition-colors",
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
        <div className="mb-3 flex min-h-8 items-center gap-3">
          {title && <h2 id={id} className="text-sm font-semibold text-foreground">{title}</h2>}
          {actions && <div className="ml-auto flex items-center gap-2">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  )
}

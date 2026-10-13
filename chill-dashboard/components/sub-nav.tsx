"use client"

import Link from "next/link"
import { usePathname, useSearchParams } from "next/navigation"

// Related screens as tabs. The most specific matching link (path plus its query) is current.
export function SubNav({label,links}:{label:string;links:[href:string,title:string][]}) {
  const pathname = usePathname(), params = useSearchParams()
  const parse = (href:string) => new URL(href, "http://local")
  const matches = (href:string) => { const url = parse(href); return url.pathname === pathname && [...url.searchParams].every(([key, value]) => params.get(key) === value) }
  const active = links.map(([href]) => href).filter(matches).sort((a, b) => parse(b).searchParams.size - parse(a).searchParams.size)[0]
  return <nav className="subnav" aria-label={label}>{links.map(([href,title]) => <Link key={href} href={href} aria-current={href === active ? "page" : undefined}>{title}</Link>)}</nav>
}

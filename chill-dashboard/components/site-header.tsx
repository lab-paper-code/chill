"use client"

import Link from "next/link"
import { usePathname } from "next/navigation"
import { useTheme } from "next-themes"
import { ChevronRight, Moon, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"
import { SidebarTrigger } from "@/components/ui/sidebar"
import { AccountControl } from "@/lib/client"
import { isActive, navigation } from "@/components/app-sidebar"

const subLabels:Record<string,string> = {new:"등록",edit:"수정",infer:"추론",history:"판단 기록",power:"전력 기록",compare:"판단 기록 비교",check:"측정 계획 확인"}

function crumbs(pathname:string) {
  const section = navigation.find(item => item.url !== "/" && isActive(item.url, pathname)) ?? navigation[0]
  const rest = pathname.split("/").filter(Boolean).slice(1)
  // Measurement detail paths carry kind/namespace before the name.
  const names = section.url === "/measurements" && rest.length === 3 ? [rest[2]] : rest
  return {section, trail:names.map(part => subLabels[part] ?? decodeURIComponent(part))}
}

export function SiteHeader() {
  const {resolvedTheme,setTheme} = useTheme()
  const pathname = usePathname()
  const {section,trail} = crumbs(pathname)
  return <header className="sticky top-0 z-20 flex h-(--header-height) shrink-0 items-center gap-2 border-b bg-background/85 backdrop-blur md:rounded-t-2xl">
    <div className="flex w-full min-w-0 items-center gap-3 px-4 lg:px-6">
      <SidebarTrigger className="-ml-1 size-9" aria-label="메뉴 열기" />
      <nav aria-label="현재 위치" className="min-w-0"><ol className="flex min-w-0 items-center gap-1.5 text-[0.9375rem]">
        <li className="flex items-center gap-2 font-semibold">{trail.length ? <Link href={section.url} className="hover:text-primary">{section.title}</Link> : <span aria-current="page">{section.title}</span>}</li>
        {trail.map((label, index) => <li key={index} className="flex min-w-0 items-center gap-1.5 text-muted-foreground"><ChevronRight className="size-4 shrink-0" aria-hidden="true" /><span className="truncate" aria-current={index === trail.length - 1 ? "page" : undefined}>{label}</span></li>)}
      </ol></nav>
      <div className="ml-auto flex items-center gap-2">
        <AccountControl />
        <Button variant="ghost" size="icon" onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")} aria-label={resolvedTheme === "dark" ? "밝은 화면으로 바꾸기" : "어두운 화면으로 바꾸기"} title="화면 테마 (D)"><Sun className="hidden dark:block" /><Moon className="dark:hidden" /></Button>
      </div>
    </div>
  </header>
}

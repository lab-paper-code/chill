"use client"

import { useTheme } from "next-themes"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { SidebarTrigger } from "@/components/ui/sidebar"

export function SiteHeader() {
  const {resolvedTheme,setTheme} = useTheme()
  return <header className="flex h-(--header-height) shrink-0 items-center gap-2"><div className="flex w-full items-center gap-2 px-4 lg:px-6"><SidebarTrigger className="-ml-1" aria-label="메뉴 열기" /><Separator orientation="vertical" className="mx-2 h-4" /><span className="text-base font-semibold">CHILL</span><div className="ml-auto"><Button variant="ghost" size="sm" onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}>화면 테마</Button></div></div></header>
}

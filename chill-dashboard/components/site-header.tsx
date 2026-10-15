"use client"

import { usePathname } from "next/navigation"
import { useTheme } from "next-themes"
import { Moon, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"
import { SidebarTrigger } from "@/components/ui/sidebar"
import { AccountControl } from "@/lib/client"
import { sectionOf } from "@/components/app-sidebar"

// Screens carry their own back link; the header only names the section.
export function SiteHeader() {
  const {resolvedTheme,setTheme} = useTheme()
  const section = sectionOf(usePathname())
  return <header className="sticky top-0 z-20 flex h-(--header-height) shrink-0 items-center gap-2 border-b bg-background/85 backdrop-blur md:rounded-t-2xl">
    <div className="flex w-full min-w-0 items-center gap-3 px-4 lg:px-6">
      <SidebarTrigger className="-ml-1 size-9" aria-label="메뉴 열기" />
      <span className="font-semibold">{section.title}</span>
      <div className="ml-auto flex items-center gap-2">
        <AccountControl />
        <Button variant="ghost" size="icon" onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")} aria-label={resolvedTheme === "dark" ? "밝은 화면으로 바꾸기" : "어두운 화면으로 바꾸기"} title="화면 테마 (D)"><Sun className="hidden dark:block" /><Moon className="dark:hidden" /></Button>
      </div>
    </div>
  </header>
}

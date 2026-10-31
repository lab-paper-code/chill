"use client"

import { useTheme } from "next-themes"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { Moon, Sun } from "lucide-react"
import { Button } from "@/components/ui/button"
import { SidebarTrigger } from "@/components/ui/sidebar"
import { AccountControl } from "@/lib/client"
import { EvaluationStatusCell } from "@/features/evaluation-active"

// The global header contains controls; each screen owns its single title.
export function SiteHeader() {
  const {resolvedTheme,setTheme} = useTheme(),pathname=usePathname()
  return <header className="sticky top-0 z-20 flex h-(--header-height) shrink-0 items-center gap-2 border-b bg-background/85 backdrop-blur md:rounded-t-2xl">
    <div className="flex w-full min-w-0 items-center gap-3 px-4 lg:px-6">
      <SidebarTrigger className="-ml-1 size-9" aria-label="메뉴 열기" />
      {pathname!=="/"&&!pathname.startsWith("/experiments")&&<Link href="/experiments" className="hidden min-w-0 truncate text-sm md:block" aria-label="실험 상태"><EvaluationStatusCell/></Link>}
      <div className="ml-auto flex items-center gap-2">
        <Link href="/?open=terms" className="text-sm underline underline-offset-4">용어 도움말</Link>
        <AccountControl />
        <Button variant="ghost" size="icon" onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")} aria-label={resolvedTheme === "dark" ? "밝은 화면으로 바꾸기" : "어두운 화면으로 바꾸기"} title="화면 테마 (D)"><Sun className="hidden dark:block" /><Moon className="dark:hidden" /></Button>
      </div>
    </div>
  </header>
}

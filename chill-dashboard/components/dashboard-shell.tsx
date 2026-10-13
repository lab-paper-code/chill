"use client"

import { AppSidebar } from "@/components/app-sidebar"
import { SiteHeader } from "@/components/site-header"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { SessionProvider } from "@/lib/client"

export function DashboardShell({children}:{children:React.ReactNode}) {
  return <SessionProvider><SidebarProvider style={{"--sidebar-width":"15.5rem","--header-height":"3.75rem"} as React.CSSProperties}><AppSidebar variant="inset" /><SidebarInset><a href="#main-content" className="sr-only focus:not-sr-only focus:p-3">본문으로 건너뛰기</a><SiteHeader /><main id="main-content" className="@container/main flex flex-1 flex-col min-w-0" tabIndex={-1}>{children}</main></SidebarInset></SidebarProvider></SessionProvider>
}

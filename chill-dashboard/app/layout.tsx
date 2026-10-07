import type { Metadata } from "next"
import "./globals.css"
import { ThemeProvider } from "@/components/theme-provider"
import { DashboardShell } from "@/components/dashboard-shell"

export const metadata:Metadata = {title:"CHILL Dashboard",description:"CHILL 원본 상태 · 관리 · 요청 관측"}
export default function RootLayout({children}:{children:React.ReactNode}) {
  return <html lang="ko" suppressHydrationWarning><body><ThemeProvider><DashboardShell>{children}</DashboardShell></ThemeProvider></body></html>
}

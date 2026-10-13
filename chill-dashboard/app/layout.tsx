import type { Metadata } from "next"
import { Asta_Sans } from "next/font/google"
import "./globals.css"
import { ThemeProvider } from "@/components/theme-provider"
import { DashboardShell } from "@/components/dashboard-shell"

// Asta Sans ships Hangul; unicode-range files load only the glyphs a page uses.
// No metric overrides exist for Asta Sans; the CSS stack supplies the fallback.
const asta = Asta_Sans({subsets:["latin"],variable:"--font-asta",display:"swap",adjustFontFallback:false})

export const metadata:Metadata = {title:"CHILL Dashboard",description:"CHILL 원본 상태 · 관리 · 요청 관측"}
export default function RootLayout({children}:{children:React.ReactNode}) {
  return <html lang="ko" className={asta.variable} suppressHydrationWarning><body><ThemeProvider><DashboardShell>{children}</DashboardShell></ThemeProvider></body></html>
}

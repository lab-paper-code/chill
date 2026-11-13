import type { Metadata, Viewport } from "next"
import localFont from "next/font/local"
import "./globals.css"
import { ThemeProvider } from "@/components/theme-provider"

// Pretendard Variable from the `pretendard` package, copied into the repo so the build needs no network.
const pretendard = localFont({ src: "./fonts/PretendardVariable.woff2", variable: "--font-pretendard", display: "swap", weight: "45 920" })

// `text-scale`: browsers that support it apply the OS text size to rem units (the root font size is never set).
export const metadata: Metadata = { title: { template: "%s | CHILL", default: "CHILL" }, other: { "text-scale": "scale" } }
export const viewport: Viewport = { colorScheme: "light dark" }

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="ko" className={pretendard.variable} suppressHydrationWarning>
      <body><ThemeProvider>{children}</ThemeProvider></body>
    </html>
  )
}

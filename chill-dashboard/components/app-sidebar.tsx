"use client"

// Adapted from the pinned dashboard-01 block; only CHILL routes, no demo account.
import Link from "next/link"
import { usePathname } from "next/navigation"
import { BrainCircuit, FlaskConical, Gauge, History, Layers, SlidersHorizontal } from "lucide-react"
import { Sidebar, SidebarContent, SidebarHeader, SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar"
import { BrandMark } from "@/components/brand"

// Every screen belongs to exactly one section by its longest path prefix.
export const navigation = [
  {title:"개요",url:"/",icon:Gauge,prefixes:[] as string[]},
  {title:"측정",url:"/measurements",icon:FlaskConical,prefixes:["/measurements","/scheduler/profiles"]},
  {title:"실험",url:"/experiments",icon:FlaskConical,prefixes:["/experiments"]},
  {title:"서비스",url:"/workloads",icon:Layers,prefixes:["/workloads"]},
  {title:"운영",url:"/scheduler",icon:BrainCircuit,prefixes:["/operations","/scheduler","/transitions","/resources","/pools"]},
  {title:"기록",url:"/records",icon:History,prefixes:["/records","/observations","/changes","/scheduler/history","/scheduler/power","/scheduler/compare"]},
  {title:"설정",url:"/settings",icon:SlidersHorizontal,prefixes:["/settings","/models","/preprocessors","/datasets","/configuration"]},
]
const under = (prefix:string, pathname:string) => pathname === prefix || pathname.startsWith(`${prefix}/`)
// The longest matching prefix wins, so /scheduler/history belongs to 기록, not 운영.
export function sectionOf(pathname:string) {
  let best = navigation[0], length = 0
  for (const item of navigation) for (const prefix of item.prefixes) if (under(prefix, pathname) && prefix.length > length) { best = item; length = prefix.length }
  return best
}

export function AppSidebar({...props}:React.ComponentProps<typeof Sidebar>) {
  const pathname = usePathname()
  const current = sectionOf(pathname)
  const {setOpenMobile} = useSidebar()
  return <Sidebar collapsible="offcanvas" {...props}>
    <SidebarHeader className="px-3 pt-4"><Link href="/" onClick={() => setOpenMobile(false)} className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 text-lg font-bold tracking-tight"><BrandMark className="size-6 text-primary" />CHILL</Link></SidebarHeader>
    <SidebarContent className="px-2 pt-2"><nav aria-label="메뉴"><SidebarMenu className="gap-1">{navigation.map(item => {
      const active = item === current
      return <SidebarMenuItem key={item.url}><SidebarMenuButton asChild isActive={active} className="h-11 gap-3 text-base data-[active=true]:bg-card data-[active=true]:font-semibold data-[active=true]:text-primary data-[active=true]:shadow-[inset_3px_0_0_var(--primary)] dark:data-[active=true]:bg-sidebar-accent"><Link href={item.url} aria-current={active ? "page" : undefined} onClick={() => setOpenMobile(false)}><item.icon /><span>{item.title}</span></Link></SidebarMenuButton></SidebarMenuItem>
    })}</SidebarMenu></nav></SidebarContent>
  </Sidebar>
}

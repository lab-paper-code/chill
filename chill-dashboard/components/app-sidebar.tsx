"use client"

// Adapted from the pinned dashboard-01 block; only CHILL routes, no demo account.
import Link from "next/link"
import { usePathname } from "next/navigation"
import { Activity, Box, Boxes, BrainCircuit, Cpu, FlaskConical, Gauge, History, Layers, SlidersHorizontal, TestTubes, Workflow } from "lucide-react"
import { Sidebar, SidebarContent, SidebarGroup, SidebarGroupLabel, SidebarHeader, SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar"
import { BrandMark } from "@/components/brand"

// Groups follow the operator's job: run services, check evidence and resources, read records, change definitions.
export const navigation = [
  {title:"홈",url:"/",icon:Gauge,group:"운영"},
  {title:"서비스",url:"/workloads",icon:Layers,group:"운영"},
  {title:"운영 판단",url:"/scheduler",icon:BrainCircuit,group:"운영"},
  {title:"변경 진행",url:"/transitions",icon:Workflow,group:"운영"},
  {title:"측정 결과",url:"/measurements",icon:FlaskConical,group:"근거와 자원"},
  {title:"장비",url:"/resources",icon:Cpu,group:"근거와 자원"},
  {title:"실행 그룹",url:"/pools",icon:Boxes,group:"근거와 자원"},
  {title:"요청 기록",url:"/observations",icon:Activity,group:"기록"},
  {title:"변경 이력",url:"/changes",icon:History,group:"기록"},
  {title:"모델",url:"/models",icon:Box,group:"정의와 설정"},
  {title:"운영 설정",url:"/configuration",icon:SlidersHorizontal,group:"정의와 설정"},
  {title:"실험",url:"/experiments",icon:TestTubes,group:"연결 준비 중"},
]
export const isActive = (url:string, pathname:string) => url === "/" ? pathname === "/" : pathname === url || pathname.startsWith(`${url}/`)

export function AppSidebar({...props}:React.ComponentProps<typeof Sidebar>) {
  const pathname = usePathname()
  const {setOpenMobile} = useSidebar()
  return <Sidebar collapsible="offcanvas" {...props}>
    <SidebarHeader className="px-3 pt-4"><Link href="/" onClick={() => setOpenMobile(false)} className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 text-lg font-bold tracking-tight"><BrandMark className="size-6 text-primary" />CHILL</Link></SidebarHeader>
    <SidebarContent><nav aria-label="메뉴">{["운영","근거와 자원","기록","정의와 설정","연결 준비 중"].map(group => <SidebarGroup key={group}><SidebarGroupLabel className="text-[0.8125rem] font-semibold text-muted-foreground">{group}</SidebarGroupLabel><SidebarMenu>{navigation.filter(item => item.group === group).map(item => {
      const active = isActive(item.url, pathname)
      return <SidebarMenuItem key={item.url}><SidebarMenuButton asChild isActive={active} className="h-9 gap-3 text-[0.9375rem] data-[active=true]:bg-card data-[active=true]:font-semibold data-[active=true]:text-primary data-[active=true]:shadow-[inset_3px_0_0_var(--primary)] dark:data-[active=true]:bg-sidebar-accent"><Link href={item.url} aria-current={active ? "page" : undefined} onClick={() => setOpenMobile(false)}><item.icon /><span>{item.title}</span></Link></SidebarMenuButton></SidebarMenuItem>
    })}</SidebarMenu></SidebarGroup>)}</nav></SidebarContent>
  </Sidebar>
}

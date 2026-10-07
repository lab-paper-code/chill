"use client"

// Adapted from the pinned dashboard-01 block; only CHILL routes, no demo account.
import Link from "next/link"
import { usePathname } from "next/navigation"
import { Activity, Box, ChartNoAxesCombined, Database, FlaskConical, History, Layers, Settings, Workflow } from "lucide-react"
import { Sidebar, SidebarContent, SidebarGroup, SidebarGroupLabel, SidebarHeader, SidebarMenu, SidebarMenuButton, SidebarMenuItem, useSidebar } from "@/components/ui/sidebar"

const navigation = [
  {title:"홈",url:"/",icon:Activity,group:"현황"},
  {title:"모델",url:"/models",icon:Box,group:"관리"},
  {title:"서비스",url:"/workloads",icon:Layers,group:"관리"},
  {title:"장비",url:"/resources",icon:Database,group:"현황"},
  {title:"실행 그룹",url:"/pools",icon:Layers,group:"현황"},
  {title:"측정 결과",url:"/measurements",icon:FlaskConical,group:"현황"},
  {title:"운영 설정",url:"/configuration",icon:Settings,group:"연결 준비 중"},
  {title:"전환 기록",url:"/transitions",icon:Workflow,group:"연결 준비 중"},
  {title:"요청 기록",url:"/observations",icon:ChartNoAxesCombined,group:"현황"},
  {title:"실험",url:"/experiments",icon:FlaskConical,group:"연결 준비 중"},
  {title:"변경 이력",url:"/changes",icon:History,group:"현황"},
]
export function AppSidebar({...props}:React.ComponentProps<typeof Sidebar>) {
  const pathname = usePathname()
  const {setOpenMobile} = useSidebar()
  return <Sidebar collapsible="offcanvas" {...props}>
    <SidebarHeader><SidebarMenu><SidebarMenuItem><SidebarMenuButton asChild><Link href="/" onClick={() => setOpenMobile(false)}><Activity /><span className="text-base font-semibold">CHILL</span></Link></SidebarMenuButton></SidebarMenuItem></SidebarMenu></SidebarHeader>
    <SidebarContent><nav aria-label="메뉴">{["관리","현황","연결 준비 중"].map(group => <SidebarGroup key={group}><SidebarGroupLabel>{group}</SidebarGroupLabel><SidebarMenu>{navigation.filter(item => item.group === group).map(item => <SidebarMenuItem key={item.url}><SidebarMenuButton asChild isActive={item.url === "/" ? pathname === "/" : pathname.startsWith(item.url)}><Link href={item.url} onClick={() => setOpenMobile(false)}><item.icon /><span>{item.title}</span></Link></SidebarMenuButton></SidebarMenuItem>)}</SidebarMenu></SidebarGroup>)}</nav></SidebarContent>
  </Sidebar>
}

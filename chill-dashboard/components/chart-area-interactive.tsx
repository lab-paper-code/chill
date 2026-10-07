"use client"

import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts"
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart"

const chartConfig={quantile:{label:"지연 시간 (ms)",color:"var(--chart-2)"}} satisfies ChartConfig
export function ChartAreaInteractive({data}:{data:{definition:string;label?:string;quantileNanos:number|null}[]}) {
  if(!data.some(row=>row.quantileNanos!==null))return <p>지연 시간 표본이 없습니다.</p>
  return <section className="space-y-3"><h3 className="font-semibold">적용 설정별 지연 시간</h3><ChartContainer config={chartConfig} className="aspect-auto h-[250px] w-full"><BarChart data={data.map((row,index)=>({definition:row.label??`설정 ${index+1}`,quantile:row.quantileNanos===null?null:row.quantileNanos/1e6}))}><CartesianGrid vertical={false}/><XAxis dataKey="definition" tickLine={false} axisLine={false} tickMargin={8}/><YAxis unit="ms"/><ChartTooltip cursor={false} content={<ChartTooltipContent indicator="dot"/>}/><Bar dataKey="quantile" fill="var(--color-quantile)"/></BarChart></ChartContainer></section>
}

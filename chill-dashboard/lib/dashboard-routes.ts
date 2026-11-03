// UI addresses only; API scheduler and measurement paths are unchanged.
export const dashboardRedirects = [
  {source:"/scheduler",destination:"/"},
  {source:"/operations/flow",destination:"/"},
  {source:"/resources",destination:"/operations/devices"},
  {source:"/pools",destination:"/operations/devices"},
  {source:"/scheduler/profiles",destination:"/measurements/results?kind=throughput"},
  {source:"/scheduler/candidates",destination:"/?evidence=candidates"},
  {source:"/scheduler/estimates",destination:"/?evidence=estimates"},
  {source:"/scheduler/load",destination:"/?evidence=load"},
  {source:"/scheduler/capacity",destination:"/?evidence=capacity"},
  {source:"/scheduler/history",destination:"/records"},
  {source:"/observations",destination:"/records?kind=requests"},
  {source:"/transitions",destination:"/records?filter=action"},
  {source:"/scheduler/power",destination:"/records?kind=power"},
  {source:"/scheduler/compare",destination:"/records?compare=1"},
  {source:"/models",destination:"/settings?tab=models"},
  {source:"/preprocessors",destination:"/settings?tab=preprocessors"},
  {source:"/datasets",destination:"/settings?tab=datasets"},
  {source:"/configuration",destination:"/settings?tab=configuration"},
  {source:"/evaluations",destination:"/experiments"},
  {source:"/evaluations/load-scenarios",destination:"/experiments/load-scenarios"},
  {source:"/measurements/coverage",destination:"/measurements"},
  {source:"/measurements/operation-cost-measurements",destination:"/measurements/results?kind=operation-cost"},
  {source:"/measurements/dispatch-occupancy-measurements",destination:"/measurements/results?kind=dispatch-occupancy"},
  {source:"/measurements/new",destination:"/measurements/requests/new"},
  {source:"/measurements/runs",destination:"/measurements/requests?view=runs"},
  {source:"/measurements/group",destination:"/measurements/requests?view=runs"},
  {source:"/measurements/check",destination:"/measurements/requests/check"},
] as const

export function dashboardDestination(pathname:string,search:URLSearchParams):string|null {
  let destination:string|undefined=dashboardRedirects.find(row=>row.source===pathname)?.destination
  const service=pathname.match(/^\/workloads\/([^/]+)\/(definition|evidence|placement|operation|changes)$/)
  if(service){const anchors:Record<string,string>={definition:"definition",evidence:"definition",placement:"placement",operation:"placement",changes:"application"};return `/workloads/${service[1]}${search.size?`?${search}`:""}#${anchors[service[2]]}`}
  if(pathname.startsWith("/transitions/")&&pathname.split("/").length===3){
    const next=new URLSearchParams(search);next.set("id",decodeURIComponent(pathname.split("/")[2]));next.set("filter","action")
    if(!next.has("view")&&!["offset","kind","at"].some(key=>next.has(key)))next.set("view","current")
    return `/records?${next}`
  }
  if(pathname==="/changes")destination=search.get("scope")==="scheduler"?"/records?filter=settings&scope=scheduler":"/records?scope=management"
  if(pathname==="/experiments"||pathname==="/evaluations") {
    const next=new URLSearchParams(search),name=next.get("name")
    if(next.get("view")!=="published"&&["a","al","as","b","bs"].some(key=>next.has(key))) {
      if(next.get("a"))return `/experiments/results?${next}`
      next.set("view","published");if(!next.has("offset"))next.set("offset","0")
      return `/experiments?${next}`
    }
    if(name&&next.get("view")!=="published") {
      next.delete("name")
      return `/experiments/runs/${encodeURIComponent(name)}${next.size?`?${next}`:""}`
    }
  }
  if(pathname==="/experiments/results"&&!search.get("a")) {
    const next=new URLSearchParams(search)
    next.set("view","published");if(!next.has("offset"))next.set("offset","0")
    return `/experiments?${next}`
  }
  const edit=pathname.match(/^\/experiments\/([^/]+)\/edit$/)
  if(edit&&decodeURIComponent(edit[1])!=="runs")return `/experiments/runs/${encodeURIComponent(decodeURIComponent(edit[1]))}/edit${search.size?`?${search}`:""}`
  if(pathname.startsWith("/experiments/")&&pathname.split("/").length===3&&!["results","runs","new","load-scenarios"].includes(pathname.split("/")[2])) {
    const next=new URLSearchParams(search);next.set("a",decodeURIComponent(pathname.split("/")[2]));return `/experiments/results?${next}`
  }
  if(pathname.startsWith("/pools/")&&pathname.split("/").length===3) {
    const next=new URLSearchParams(search),uid=decodeURIComponent(pathname.split("/")[2]),open=next.get("open")
    next.set("workload",uid)
    if(open?.startsWith("node:")){next.set("node",open.slice(5));next.delete("open");return `/operations/devices?${next}`}
    if(!open)next.set("open",`service:${uid}`)
    return `/?${next}`
  }
  const segments=pathname.split("/")
  if(segments.length===5&&segments[1]==="measurements"&&["operation-cost-measurements","dispatch-occupancy-measurements"].includes(segments[2])) {
    const next=new URLSearchParams(search)
    next.set("kind",segments[2]==="operation-cost-measurements"?"operation-cost":"dispatch-occupancy")
    next.set("namespace",decodeURIComponent(segments[3]));next.set("measurement",decodeURIComponent(segments[4]))
    return `/measurements/results?${next}`
  }
  if(segments.length===5&&segments[1]==="measurements"&&["batch-energy-optimizations","profiling-runs"].includes(segments[2])) {
    const next=new URLSearchParams(search);next.set("namespace",decodeURIComponent(segments[3]))
    if(segments[2]==="profiling-runs")next.set("run",decodeURIComponent(segments[4]))
    return `/measurements/requests${segments[2]==="batch-energy-optimizations"?`/${segments[4]}`:""}?${next}`
  }
  if(!destination)return null
  const [path,defaults]=destination.split("?"),next=new URLSearchParams(search)
  for(const [key,value] of new URLSearchParams(defaults))next.set(key,value)
  return `${path}${next.size?`?${next}`:""}`
}

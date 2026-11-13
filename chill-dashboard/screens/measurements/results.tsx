"use client"

import Link from "next/link"
import { buttonClassName, Page, PageHeader, PageTabs, type Segment } from "@/components/kit"
import { useSession } from "@/lib/client"
import { occupancyRequestHref } from "@/lib/dispatch-occupancy"
import { sectionTabs } from "@/lib/routes"
import { MeasurementCostResults } from "./results-cost"
import { MeasurementOccupancyResults } from "./results-occupancy"
import { resultKinds, useUrlState, type ResultKind } from "./results-shared"
import { MeasurementThroughputResults } from "./results-throughput"

export function MeasurementResultsScreen() {
  const { search, set } = useUrlState()
  const { config } = useSession()
  const kind: ResultKind = resultKinds.find(item => item.value === search.get("kind"))?.value ?? "throughput"
  // Switching kind drops every selection, filter and page of the previous kind.
  const lead: Segment = {
    label: "종류", value: kind, options: resultKinds.map(({ value, label }) => ({ value, label })),
    onChange: value => set({ kind: value, profile: null, measurement: null, uid: null, op: null, offset: null, model: null, node: null, status: null }),
  }
  const occupancy = kind === "dispatch-occupancy"
  return (
    <Page>
      <PageHeader
        title="측정"
        actions={<Link className={buttonClassName({ variant: "default" })} href={occupancy ? occupancyRequestHref(config.namespace) : "/measurements/requests/new"}>{occupancy ? "새 점유시간 측정 요청" : "새 측정 요청"}</Link>}
      />
      <PageTabs tabs={sectionTabs.measurements} />
      {kind === "throughput" && <MeasurementThroughputResults lead={lead} />}
      {kind === "operation-cost" && <MeasurementCostResults lead={lead} namespace={config.namespace} />}
      {occupancy && <MeasurementOccupancyResults lead={lead} namespace={config.namespace} />}
    </Page>
  )
}

"use client"

import { TimeSeries } from "@/components/kit"
import type { Spec } from "@/lib/evaluation-author"
import { scenarioServices, scenarioSteps } from "@/lib/experiments-scenario"

/** Request rate per service as a step line over the scenario time (seconds from start). */
export function RateChart({ spec, height }: { spec: Spec; height?: number }) {
  return (
    <TimeSeries
      label="서비스별 요청률" unit="req/s" x="seconds" curve="step" legend height={height} data={scenarioSteps(spec)}
      series={scenarioServices(spec).map((name, index) => ({ key: `s${index}`, label: name || `서비스 ${index + 1}` }))}
    />
  )
}

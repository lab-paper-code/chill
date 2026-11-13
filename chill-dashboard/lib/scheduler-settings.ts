import type { SchedulerSettings } from "./read-types.ts"

// Public editable contract: internal/serving/scheduler.go SchedulerSettings.
// Runtime declarations returned alongside GET settings are not editable here.
// Labels and one-sentence tooltips are shared by the scheduler settings screen, the experiment editor and run comparisons.
export type NumericSettingKey = { [K in keyof SchedulerSettings]-?: NonNullable<SchedulerSettings[K]> extends number | bigint ? K : never }[keyof SchedulerSettings]
export type SettingField = {key:Exclude<NumericSettingKey,"requestTimeout">|"planSearchStrategy"|"planPostFilterFinalists"|"planPricedStart";label:string;help:string;group:"cycle"|"limit"|"capacity"|"search";unit?:string;kind?:"strategy"|"boolean";defaultValue?:string|boolean|number}
export const settingFields: SettingField[] = [
  {key:"observationWindowNanos",label:"관측 구간",help:"판단에 쓰는 최근 요청 부하의 길이",group:"cycle"},
  {key:"controlIntervalNanos",label:"판단 주기",help:"새로 판단하는 간격",group:"cycle"},
  {key:"comparisonPeriodNanos",label:"에너지 비교 기간",help:"유지안과 후보의 에너지를 비교하는 고정 기간",group:"cycle"},
  {key:"transitionReserveFraction",label:"전환 예비 비율",help:"전환 중 수요에 더해 확보할 용량 비율 (0~1)",group:"cycle"},
  {key:"scaleDownHeadroomFraction",label:"하강 여유",help:"줄인 구성이 추가로 감당할 수요 비율 (0~1)",group:"cycle"},
  {key:"servingMemoryBytes",label:"실행 메모리",help:"인스턴스마다 모델 메모리에 더하는 메모리",group:"limit"},
  {key:"maxReplicas",label:"복제 수 상한",help:"서비스별 복제 수 탐색 상한",group:"limit",unit:"개"},
  {key:"maxInstances",label:"인스턴스 수 상한",help:"전체 인스턴스 수 탐색 상한",group:"limit",unit:"개"},
  {key:"completionMarginMS",label:"완료 여유",help:"결합 관측 표본이 없을 때 쓰는 경로 예산 초기값",group:"capacity",unit:"ms"},
  {key:"capacityWindowNanos",label:"창 길이",help:"창마다 재생하는 최소 요청 시간",group:"capacity"},
  {key:"capacityWindows",label:"창 수",help:"요청률마다 재생하는 독립 창 수",group:"capacity",unit:"개"},
  {key:"capacityPassingWindows",label:"충족 창 수",help:"이만큼의 창이 목표를 지키면 그 요청률을 감당한다고 판정",group:"capacity",unit:"개"},
  {key:"capacityMinimumRequests",label:"최소 요청 수",help:"창마다 재생하는 최소 요청 수",group:"capacity",unit:"건"},
  {key:"capacityGridSteps",label:"격자 수",help:"오프라인 용량표에서 최대 처리율을 나누는 수",group:"capacity",unit:"개"},
  {key:"capacityBisectionWidth",label:"이분 탐색 폭",help:"오프라인 용량표 탐색 종료 폭, 최대 처리율 대비 비율",group:"capacity"},
  {key:"capacityTailSamples",label:"꼬리 표본 수",help:"목표 분위수 위로 확보할 창별 표본 수",group:"capacity",unit:"개"},
  {key:"planSearchStrategy",label:"스케줄러",help:"v1은 기존 구성 탐색, v2는 한계비용 탐욕 탐색",group:"search",kind:"strategy"},
  {key:"planSearchWorkers",label:"동시 계산 수",help:"후보 구성을 동시에 계산하는 수",group:"search",unit:"개"},
  {key:"planSearchEvaluations",label:"평가 예산",help:"v2 증분 평가 횟수 상한",group:"search",unit:"회",defaultValue:500000},
  {key:"planExactFinalists",label:"정확 검증 후보 수",help:"증분 평가 상위 후보 중 정확히 검증하는 수",group:"search",unit:"개",defaultValue:16},
  {key:"planPolishActive",label:"연마 활성 계획 수",help:"결합 연마에서 살펴볼 활성 계획 수, 0은 전체",group:"search",unit:"개",defaultValue:8},
  {key:"planPostFilterFinalists",label:"후처리 뒤 후보 비교",help:"실행 수용 조건을 적용한 뒤 후보를 비교",group:"search",kind:"boolean",defaultValue:false},
  {key:"planPricedStart",label:"가격 유도 출발점",help:"v2 탐색에 추가 출발점을 포함",group:"search",kind:"boolean",defaultValue:true},
]
export const settingControls: {key:keyof SchedulerSettings["controls"];label:string}[] = [
  {key:"placement",label:"배치 위치"},{key:"resources",label:"자원 할당"},
  {key:"replicas",label:"복제 수"},{key:"routing",label:"요청 분배"},
  {key:"batching",label:"배칭"},{key:"power",label:"전력 모드"},{key:"membership",label:"실행 그룹 소속"},
]
export const settingTargets = [
  {key:"autonomousWorkloads",label:"자동 제어 서비스",help:"선택한 서비스의 구성을 스케줄러가 바꿈"},
  {key:"minimumDemand",label:"최소 수요",help:"서비스별 계획 수요의 하한, 비우면 미설정"},
  {key:"excludedNodes",label:"제외 장치",help:"스케줄러가 배치, 제거, 전력 모드를 바꾸지 않는 장치"},
] as const
export const editableSettingKeys = [...settingFields.map(field=>field.key),"controls",...settingTargets.map(field=>field.key),"sloPriorityStrategy","workloadPriority"] as const
// Server settings without editing controls still cross PUT and conflict checks.
export const preservedSettingKeys = ["defaultOperationCost","defaultIdlePower","defaultDispatchOccupancyNanos"] as const
export const writableSettingKeys = [...editableSettingKeys,...preservedSettingKeys] as const

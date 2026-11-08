import type { SchedulerSettings } from "./read-types.ts"

// Public editable contract: internal/serving/scheduler.go SchedulerSettings.
// Runtime declarations returned alongside GET settings are not editable here.
export type NumericSettingKey = { [K in keyof SchedulerSettings]-?: NonNullable<SchedulerSettings[K]> extends number | bigint ? K : never }[keyof SchedulerSettings]
export type SettingField = {key:Exclude<NumericSettingKey,"requestTimeout">|"observedPathBudgets"|"planSearchStrategy"|"planPostFilterFinalists"|"planPricedStart";label:string;help:string;group:"cycle"|"limit"|"capacity"|"advanced";unit?:string;kind?:"strategy"|"boolean";defaultValue?:string|boolean|number}
export const settingFields: SettingField[] = [
  {key:"observationWindowNanos",label:"부하를 살펴볼 구간",help:"판단할 때 최근 요청 부하를 이만큼 살펴봐요.",group:"cycle"},
  {key:"controlIntervalNanos",label:"운영 판단 주기",help:"이 간격마다 새로 판단해요.",group:"cycle"},
  {key:"comparisonPeriodNanos",label:"에너지 비교 기간",help:"유지와 모든 후보를 같은 고정 기간으로 비교해요. 전환 동작 비용과 기간 안의 서빙 소비를 반영해요.",group:"cycle"},
  {key:"transitionReserveFraction",label:"전환 예비 비율",help:"전환 중 실제 준비된 용량으로 수요와 예비를 감당해요. 0은 별도 예비 없음, 범위는 0~1이에요.",group:"cycle"},
  {key:"scaleDownHeadroomFraction",label:"하강 용량 여유",help:"최종 하강 경로가 현재 유효 수요의 (1 + 비율)을 감당해야 해요. 0은 추가 여유 없음, 범위는 0~1이에요. 회복이나 중간 전환 예비를 부풀리지 않아요.",group:"cycle"},
  {key:"completionMarginMS",label:"완료 여유",help:"실행이 끝난 뒤 응답까지 요청마다 이만큼 더해 예상 완료 시각을 계산해요.",group:"capacity",unit:"ms"},
  {key:"observedPathBudgets",label:"관측 경로 예산 적용",help:"관측한 TOTAL·POST 예산을 용량 계산과 새 요청에 적용해요. 끄면 완료 여유 설정을 사용하고 관측 수집과 원본 기록은 유지해요. 생략 시 사용해요.",group:"capacity",kind:"boolean",defaultValue:true},
  {key:"capacityWorkers",label:"용량 행 동시 계산 수",help:"0은 자동으로 GOMAXPROCS의 절반(최소 1개), 양수는 지정한 동시 계산 수예요. CPU를 예약하지 않아 여유를 보장하지 않아요. 줄이면 실행 중인 계산은 끝까지 진행해요.",group:"capacity",unit:"개",defaultValue:0},
  {key:"servingMemoryBytes",label:"서비스 실행 메모리",help:"측정한 모델 메모리에 인스턴스마다 더하는 메모리예요. 절반을 대기 요청에 써요. 노드 자원은 배포 설정에서 관리해요.",group:"limit"},
  {key:"maxReplicas",label:"복제 수 상한",help:"서비스 복제 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"maxInstances",label:"인스턴스 수 상한",help:"인스턴스 수를 이만큼까지 살펴봐요.",group:"limit",unit:"개"},
  {key:"capacityWindowNanos",label:"용량 계산 창 길이",help:"용량을 계산할 때 창마다 적어도 이 시간만큼의 요청을 재생해요.",group:"capacity"},
  {key:"capacityWindows",label:"용량 계산 창 수",help:"요청률마다 이만큼의 독립 창을 재생해요.",group:"capacity",unit:"개"},
  {key:"capacityPassingWindows",label:"용량 충족 창 수",help:"이만큼의 창이 SLO를 지키면 그 요청률을 감당한다고 봐요. 창 수 이하로 정해요.",group:"capacity",unit:"개"},
  {key:"capacityMinimumRequests",label:"용량 계산 최소 요청 수",help:"창마다 적어도 이만큼의 요청을 재생해요.",group:"capacity",unit:"건"},
  {key:"capacityGridSteps",label:"용량 계산 격자 수",help:"최대 처리율을 이만큼 나누어 모든 요청률을 판정해요. 1 이상이에요.",group:"capacity",unit:"개"},
  {key:"capacityBisectionWidth",label:"용량 이분 탐색 폭",help:"최대 처리율에 대한 탐색 종료 폭이에요. 0 초과 1 이하예요.",group:"capacity"},
  {key:"capacityTailSamples",label:"용량 꼬리 표본 수",help:"SLO 백분위 위에 확보할 창별 표본 수예요. 1 이상이에요.",group:"capacity",unit:"개"},
  {key:"planSearchWorkers",label:"구성 탐색 동시 계산 수",help:"운영 판단이 구성 후보를 동시에 이만큼 계산해요. 탐색은 판단 주기의 절반 안에 끝나요.",group:"advanced",unit:"개"},
  {key:"planSearchStrategy",label:"스케줄러",help:"전체 노드 계획을 결정할 CHILL 스케줄러를 선택해요.",group:"advanced",kind:"strategy"},
  {key:"planSearchEvaluations",label:"구성 탐색 평가 예산",help:"v2의 증분 평가를 이 횟수까지 계산해요.",group:"advanced",unit:"회",defaultValue:500000},
  {key:"planExactFinalists",label:"정확 검증 후보 수",help:"증분 평가의 상위 후보를 이만큼 정확히 검증해요.",group:"advanced",unit:"개",defaultValue:16},
  {key:"planPolishActive",label:"결합 연마 활성 계획 수",help:"결합 연마에서 살펴볼 활성 계획 수예요. 0은 전체 계획이에요.",group:"advanced",unit:"개",defaultValue:8},
  {key:"planPostFilterFinalists",label:"후처리 뒤 후보 비교",help:"후보마다 교체 회수와 최소 이득을 적용한 뒤 비교해요.",group:"advanced",kind:"boolean",defaultValue:false},
  {key:"planPricedStart",label:"가격 유도 출발점",help:"용량 가격으로 만든 출발점도 v2 탐색에 사용해요.",group:"advanced",kind:"boolean",defaultValue:true},
]
export const settingControls: {key:keyof SchedulerSettings["controls"];label:string}[] = [
  {key:"placement",label:"배치 위치"},{key:"resources",label:"자원 할당"},
  {key:"replicas",label:"복제 수"},{key:"routing",label:"요청 분배"},
  {key:"batching",label:"배칭"},{key:"power",label:"전력모드"},{key:"membership",label:"실행 그룹 소속"},
]
export const settingTargets = [
  {key:"autonomousWorkloads",label:"CHILL 자동 운영 서비스",help:"선택한 서비스의 구성을 CHILL 운영 판단이 변경해요. 서비스 UID로 저장해요."},
  {key:"minimumDemand",label:"최소 수요",help:"서비스 UID별 계획 수요의 하한이에요. 단위는 req/s, 0 이상의 유한한 수이며 상한은 없어요. 미설정과 0을 구분해서 저장해요."},
  {key:"excludedNodes",label:"계획 제외 장비",help:"운영 판단의 배치·제거·전력모드 변경 제외 대상이에요. 중복 없는 장비 이름으로 저장해요."},
] as const
export const editableSettingKeys = [...settingFields.map(field=>field.key),"controls",...settingTargets.map(field=>field.key)] as const
// Server settings without editing controls still cross PUT and conflict checks.
export const preservedSettingKeys = ["defaultOperationCost","defaultIdlePower","defaultDispatchOccupancyNanos"] as const
export const writableSettingKeys = [...editableSettingKeys,...preservedSettingKeys] as const

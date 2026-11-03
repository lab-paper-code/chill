import type { SchedulerSettings } from "./read-types.ts"

// Public editable contract: internal/serving/scheduler.go SchedulerSettings.
// Runtime declarations returned alongside GET settings are not editable here.
export type NumericSettingKey = { [K in keyof SchedulerSettings]-?: SchedulerSettings[K] extends number | bigint ? K : never }[keyof SchedulerSettings]
export type SettingField = {key:Exclude<NumericSettingKey,"requestTimeout">;label:string;help:string;group:"cycle"|"limit"|"capacity"|"advanced";unit?:string}
export const settingFields: SettingField[] = [
  {key:"observationWindowNanos",label:"부하를 살펴볼 구간",help:"판단할 때 최근 요청 부하를 이만큼 살펴봐요.",group:"cycle"},
  {key:"controlIntervalNanos",label:"운영 판단 주기",help:"이 간격마다 새로 판단해요.",group:"cycle"},
  {key:"comparisonPeriodNanos",label:"에너지 비교 기간",help:"유지와 모든 후보를 같은 고정 기간으로 비교해요. 이보다 긴 전환은 제외해요.",group:"cycle"},
  {key:"replacementDwellNanos",label:"교체 후 최소 체류 기간",help:"같은 노드·서비스의 에너지 절약만을 위한 재교체를 보류해요. 0은 에너지 비교 기간을 사용해요. 용량 부족·고장 복구는 기다리지 않아요.",group:"cycle"},
  {key:"bootstrapCapacityWaitNanos",label:"초기 용량 대기 시간",help:"준비된 공급이 없는 서비스가 용량 근거를 기다릴 수 있는 시간이에요. 0~1시간이에요.",group:"cycle"},
  {key:"scaleDownStabilizationNanos",label:"하강 안정화 기간",help:"이 기간의 최대 유효 수요가 현재 준비된 공급보다 작아야 축소와 에너지 교체를 허용해요. 후보도 최대 수요를 감당해야 해요. 완전한 관측 창이 필요하며, 0도 현재 수요·공급 검사는 유지해요.",group:"cycle"},
  {key:"transitionReserveFraction",label:"전환 예비 비율",help:"전환 중 실제 준비된 용량으로 수요와 예비를 감당해요. 0은 별도 예비 없음, 범위는 0~1이에요.",group:"cycle"},
  {key:"scaleDownHeadroomFraction",label:"하강 용량 여유",help:"최종 하강 경로가 관측 창 최대 수요의 (1 + 비율)을 감당해야 해요. 0은 추가 여유 없음, 범위는 0~1이에요. 회복이나 중간 전환 예비를 부풀리지 않아요.",group:"cycle"},
  {key:"maxObservationAgeNanos",label:"관측을 쓸 수 있는 시간",help:"이보다 오래된 관측은 판단에 쓰지 않아요.",group:"cycle"},
  {key:"completionMarginMS",label:"완료 여유",help:"실행이 끝난 뒤 응답까지 요청마다 이만큼 더해 예상 완료 시각을 계산해요.",group:"capacity",unit:"ms"},
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

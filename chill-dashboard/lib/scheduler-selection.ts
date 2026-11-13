export const schedulerOptions = [
  {value:"v1",label:"CHILL v1",description:"기존 구성 탐색으로 전체 장치 계획 결정"},
  {value:"v2",label:"CHILL v2",description:"한계비용 탐욕 탐색으로 전체 장치 계획 결정"},
] as const

export const schedulerLabel = (value:string|undefined) => value===undefined?"기록 없음":schedulerOptions.find(option=>option.value===value)?.label??value

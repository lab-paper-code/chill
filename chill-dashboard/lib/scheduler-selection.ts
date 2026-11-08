export const schedulerOptions = [
  {value:"v1",label:"CHILL v1",description:"기존 구성 탐색으로 전체 노드 계획을 결정해요."},
  {value:"v2",label:"CHILL v2",description:"한계비용 탐욕 탐색으로 전체 노드 계획을 결정해요."},
] as const

export const schedulerLabel = (value:string|undefined) => value===undefined?"기록 없음":schedulerOptions.find(option=>option.value===value)?.label??value

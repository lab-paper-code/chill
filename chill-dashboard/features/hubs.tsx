"use client"

import { Row, Rows, Screen } from "@/components/screen"

// Section indexes: a list of screens, nothing else.
export function RecordsPage() {
  return <Screen title="기록" lede="지나간 요청, 변경, 판단과 측정을 찾아봐요.">
    <Rows label="서비스">
      <Row href="/observations" label="요청 기록" hint="서비스가 받은 요청과 처리 결과" />
      <Row href="/changes" label="서비스 변경 이력" hint="등록, 수정, 삭제한 기록" />
    </Rows>
    <Rows label="운영">
      <Row href="/scheduler/history" label="판단 기록" hint="운영 판단이 남긴 기록" />
      <Row href="/scheduler/compare" label="판단 비교" hint="두 판단을 나란히 비교" />
      <Row href="/transitions?view=history" label="변경 동작 기록" hint="실제로 수행한 구성 변경" />
      <Row href="/scheduler/power" label="전력 기록" hint="노드별 측정 전력" />
      <Row href="/changes?scope=scheduler" label="운영 설정 변경 이력" hint="누가 언제 운영 설정을 바꿨는지" />
    </Rows>
  </Screen>
}

export function SettingsPage() {
  return <Screen title="설정" lede="서비스가 쓰는 모델과 운영 방식을 정해요.">
    <Rows>
      <Row href="/models" label="모델" hint="모델 파일과 실행 방식 등록" />
      <Row href="/preprocessors" label="전처리기" hint="원본 입력을 모델 입력으로 바꾸는 파일 등록" />
      <Row href="/datasets" label="데이터셋" hint="측정과 평가에 쓰는 입력 묶음 등록" />
      <Row href="/configuration" label="운영 설정" hint="판단 주기, 자동 적용, 자원 한도" />
    </Rows>
  </Screen>
}

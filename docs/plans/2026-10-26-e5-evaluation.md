# E5 研究 평가 — 결과 요약

- 상태: completed. 최종 결과·행별 수치·factor·조건은 [E5 확정 보고서](../reports/e5-evaluation-20261030.md), 요구사항 ID·표는 [근원 명세](../spec/README.md)가 권위다.
- 7 Node(genesis2 하드웨어 결함 제외), p95 MNV1s/PLC36ms/Smol7.034s, m29ms, L2/1/75s, cap129s·delivery10ms·socket L+20ms. R MNV235.890404/PLC387.365408/Smol1.285195 req/s, 몫 .45/.1/.45, `rate=α×s×R`.
- G0 #203: 174.56req/s·120초, PLC 수신 p95 14.2ms·CPU1.60/6·throttle0로 비병목 확인. 최대 G 인증이 아니며 고정 배치 Smol SLO 실패는 별도다.
- full8행·static6행, factor12행(6요소×E5-2/5; placement는 membership과 동일 목표라 제외), 후속2회차 #222+#223(rev104)와3회차 표적 #224/#225(rev105)를 구분한다. 3회차 E5-6/7의 시작 상태 차이를 C1 효과로 귀속하지 않는다.
- D1 분모는 frozen planned offered 전체, 필요 rank=ceil(offered×percentile). 완료-only p95가 SLO 아래여도 실패·미발행·미완료 때문에 D1 FAIL일 수 있다. 수신→응답 SLO, send→수신 입구 지연, 예정→send 생성기 지각을 구분하고 L은 차감하지 않는다.
- 열린 고리·trace 전부 공급, lateness>50ms 비율≤1%; 미달은 INVALID_GENERATOR, timestamp/정책 결측은 UNKNOWN. judge는 SLO·에너지·생성기·전력 완전성만 판정하며 장부/중첩/quiet을 재검사하지 않는다.
- 행 전력: Node별 공백합≤창1%·단일≤30s면 선형 보간, 오차상한=공백초×창 내 W범위. 초과는 UNKNOWN; d/비용의3s 정책은 불변. 공유 POOL 에너지를 Workload 행으로 합산하지 않는다.
- 첫 준비·전환·Node 차단 요청도 전체 offered에 남기고 별도 구간 집계를 병기한다. E5-7은180초 Pod 삭제·360초 cordon+Pod 삭제·600초 uncordon의 대리 실험으로 실제 네트워크 단절이 아니다.
- bind·설정·승인은 실행 전 동결한다. 생성기2CPU/1Gi·shards3/1/1·seed101/202/303와 실제 소스/근거를 보존하며, 초기화/정리는 별도다. 판정 FAIL은 기록 후 계속, 운영 실패·INVALID만 중단한다.
- 독립 반복·점진 감소·다른 모델 버스트·모든 제어요소 독립 인과·전역95% 보장은 미평가다. signed d·기존 Accepted·planner max(d,0), energyBelowResolution, fresh TLS 조건과 payload 분리 UNKNOWN을 숨기지 않는다.
- 원시: `O/campaign/v2/d-20261027/control-{213,216,222,223,224,225}*`, `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`. 남은 일은 [로드맵](2026-10-26-remaining-roadmap.md).

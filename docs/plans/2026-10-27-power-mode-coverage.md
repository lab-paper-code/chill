# U2 전력모드 커버리지 — 결과 요약

- 상태: completed(승인 최소안 기록), 미달·후속 측정은 아래와 [현재 로드맵](2026-10-26-remaining-roadmap.md)에 남긴다. live 최신 inventory로 오해하지 않는다.
- 최소안: Jetson 3대 × 두 모드 × MNV/PLC/Smol CPU q1/B1 = 18조합. jetsono MAXN↔15W, jetsonx 6CORE↔4CORE, nano MAXN↔5W.
- 후보에는 실행 profile·새 규칙 d·정확 prepare/remove·idle baseline·양방향 모드 비용의 다섯 근거가 필요하다. 선언 모드와 실제 물리 모드, 정확 비용69/69와 추가 모드 적격성은 다르다.
- jetsono MAXN/15W 완료. nano5W 세 조합은 serving CPU0으로 Infeasible, 임의 예약 변경·κ0 대체 없음.
- jetsonx Smol 6CORE/4CORE는 유도 수명이 gateway cap을 초과해 ConditionsInfeasible(연구 SLO 7.034s 불가), 입력 L 축소 없음.
- jetsonx PLC4CORE d는 RequestTimeout 후 UnknownPhysicalContinuity로 두 번 실패, 자원 Rejected라 E5 입력에서 제외하고 추가 재측 없이 미완으로 보존했다. E5 뒤 U5와 재조사한다.
- Smol x 준비의 증분 에너지는 0 포함 신뢰구간에서만 명시 0+`energyBelowResolution`이며 clamp가 아니다(`331624f0`, rev99).
- U3 NVIDIA 전환은 별도 근거이며 U4 genesis2→3 공유는 5% 동등성 기준 실패로 적용하지 않는다. 최소안 외 중간/전체 자동 측정 승인은 없다.
- 원시 inventory: `O/intent/u2-power-coverage-{live,rows}-20261027.json`, `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`. 후속 승인값은 [결정 표](../intent/decision-log.md), 최종 채택 범위는 [E5 보고서](../reports/e5-evaluation-20261030.md).

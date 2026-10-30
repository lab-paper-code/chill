# 핵심 결정 일지

현행 결정만 주제별로 보존한다. 요구사항·수치의 정본은 [spec](../spec/README.md), SRS와 설계다. 뒤집힌 안과 완료 절차는 git 이력에 남긴다. 날짜는 원문의 사용자/실행 시각을 재구성하지 않으며, 커밋 미기재는 `—`로 표시한다.

## 현행 결정 표

| 주제 | 결정 | 근거·권위 | 커밋 |
|---|---|---|---|
| 연구 목표 | 이질 워크로드가 원시 요청→응답 공통 경로를 사용하고, 실측 후보에서 첫 구성을 자동 생성한다. SLO 우선·전환 포함 전체 POOL 에너지 최소화 | 모델별 전용 경로/수동 첫 구성은 일반 제어 주장이 아님; [north-star](north-star.md) | — |
| 자원 경계 | 워크로드별 인스턴스·POOL, Pod 실물 장부·영향 범위 예약, 무관 자원 판단 지속. 새 서비스·저장소·CRD 없음 | 시연2 중복 준비로 B mobilenet 성공0.67%; [ADR0023](../adr/0023-resource-ledger.md) | — |
| 관측과 실물 | 정책 ACK 대기/관측 실패를 실물 소실로 해석하지 않고 마지막 확인 상태를 유지한다. 퇴역 source의 늦은 probe는 재생성/게시하지 않는다 | 실물·앎·실행 권한 분리; [사고0002](../postmortems/0002-demo2-duplicate-preparation.md) | — |
| 빈 출발 | 초과량을 나쁜 순서로 비교하는 leximin과 전체 후보 용량 대기. bootstrapCapacityWaitNanos 기본10분·범위0~1시간; 대기 Workload Node 선점 금지는 보류 | 불가능 Workload 하나로 전 후보∞ 동점이 된 rev76 사고; [ADR0024](../adr/0024-candidate-comparison-and-bootstrap.md) | — |
| 수요·분배 | 최근 유입·명시 최소 수요를 반영하고 준비된 공급부터 배분한다. 마지막 소유 Workload 키는 명시적으로 비우며 필드 생략은 보존 | 첫 시연 B 성공63.8%, 잔존 staticRouting이 측정 seed를 가림; [scheduler SRS](../spec/scheduler/SRS.md) | — |
| 두 박자 | 증가/확장은 빠르게, 축소/에너지 교체는 안정화 뒤. 기본300초·시연120초·전환 reserve0, 비교 H600초는 후보 독립 | 느린 전환의 왕복/손실 및 후보별 기간 불공정; [ADR0025](../adr/0025-two-tempo-control.md) | — |
| 하강·빈 Node 절전 | 안정화 창의 UID별 유효 수요 최댓값·완전한 창·실제 공급 여유·후보 수용을 요구한다. 빈 Node도 일반 gain/minimumGain/payback 적용, UID 합계 상쇄 금지 | 국소 고점 timer는 실제 하강39초에 축소 허용; H600초 jetsono 이득−699J·본전약52분은 미실행. SCH-56·59/AC-42·44 | — |
| 수동 승인 | 마지막 Tick이 아닌 허가 직전 신선한 상태를 기준으로 승인하며 효과/소유 범위에 따라 예약을 처리한다 | 실제 잘못된 거절2건; [ADR0027](../adr/0027-manual-operation-admission.md) | — |
| d 정의 | d=1/X−profile 평균시간/B, X·포화는 실제 물리 batch 기준. BatchID/실행 연속성·오류 주변 유휴·창 경계 포함, 결손은 Insufficient | HTTP 성공 행은 만료 요청의 물리 실행을 누락; [측정 설계](../design/serving/measurement-api.md) | — |
| d 조건 | 대상 분포·SLO로 계산, 정밀도1%·양측95% 계획 근사·공통여유1.25·유휴1ms/요청·본창360초/준비60초 하한·전체3600초 상한 | Smol 실측약15.9초/batch와 손 추정10초 불일치; 95%는 전체 d 보장이 아님 | — |
| d 공급 | closed-loop K=min(64,bytes cap,floor((L−delivery)/(B×mu_tail))), mu_tail=동일 batch frozen profile.maxObservedSeconds. 최대값 없으면 거부, 평균 배수 대체 금지 | D34 평균 K52는 L1초 여유26ms, 관측최대37.825158ms로 K26. 미래 무실패 보장은 아님 | — |
| d 유휴·투영 | 식별된 유휴≤동결 maximumIdleFraction(승인.05,[0,1])와1ms/요청. d 차감 없이 dRange=[d−idle/request,d]·κ오차상한=idleFraction. signed raw·Accepted 유지, planner max(d,0) | 유휴는 d를 키우는 보수 오염; 미식별은 계속 탈락, 범위는95%CI 아님; [ADR0028](../adr/0028-identified-idle-acceptance.md) | — |
| 에너지 이득 | 위상·결측 민감도 B-only, 동일 근거 상쇄·전환 chain·불가능 구성 회복 유지, profile CI 추가 장벽 없음 | 공유 추정원·공분산과 맞지 않는 이중 장벽 제거; 전역95% 보장 아님; [계획기](../design/serving/analytic-planner.md) | — |
| 전력 정책 | 운영 MaximumGap3s·MinimumCoverage.75·boundary1s; E5 긴 행은 별도1%/30s. raw와 기록정책 불변, override는 표시 | 단발 공백≤2.52s·연속2실패≥3.2s; [측정 명세](../spec/00_measurement.md) | — |
| PMTC | 비용창 apply→fresh Ready+settlingSeconds(기본60,[0,3600]), 적분J와 목표baseline 차감 같은 창. tau는 Ready까지만, settling 별도 | NVIDIA15/15 PowerWindowInsufficient. 60초는 실측 안정시간 아님; raw/finalized 불변 | `52da1d38` |
| NVIDIA 모드 | GPU_1500/1200/900 min=max·mode0, fixed 별도. graphics/SM 현재값 일치+settling으로 확인하되 잠금 레지스터 증명은 아님. memory clock/power limit/runtime7 불변 | locked-range getter 없음; application clocks N/A; [PA-NV-01~05](../spec/platform/SRS.md) | — |
| NVIDIA 권한·복구 | 별도 operand root+SYS_ADMIN·nvidia/utility·read-only host tools, hostPID/privileged/SYS_BOOT 없음. 실패 복원/readback 또는 -rgc cleanup; 재부팅 뒤 조회만 | reset 종료를 unlocked 검증 성공으로 확대하지 않음; [Plan6](../design/profiling/plans/06-execution-support-expansion.md) | — |
| 측정 창 | NVIDIA idle settling15s+idle30s, 느린 Smol active observer297초+최대추론+여유(당시330초), 측정목표297초 유지 | HTTP keep-alive60초 초과 단절·마지막추론약12초로 observer 종료. 연결 재수립은 runtime 재빌드 시 과제 | — |
| 프로파일 재사용 | 같은 DeviceClass·frozen 조건, 모든 batch 활성/유휴30초×3 이상·상대차≤.05로 확인. annotation/불변 결과 보존, 거절 시 독립 측정. d/비용/전환은 Node별 | 5%는 screening이지 통계 보장 아님; [PR-01~05](../spec/catalog/SRS.md) | — |
| genesis2→3 | 공유 거절·독립 측정. 조건/digest/CPU를 완화하지 않는다 | loaded idle108.3W vs71.9W(33.65%), power limit250/300W·max clock2100/2175MHz로 비동등 | — |
| U2 커버리지 | Jetson 3대 두 모드×세 모델 CPU q1/B1 최소 18조합, 다섯 근거가 있는 후보만 채택한다. 최소안 밖 자동 확대는 없다 | nano 5W CPU0·Smol x 수명 cap 초과는 Infeasible. PLC x4C 두 번 실패·자원 Rejected는 미완; [결과](../plans/2026-10-27-power-mode-coverage.md) | — |
| 분해능 이하 에너지 | CI가 0을 포함할 때만 명시 0 J/item+energyBelowResolution, 상한도 음수면 측정 오류다. clamp·양수가 나올 때까지 반복 측정은 금지한다 | jetsonx Smol −8.96/−4.90 J/item은 idle 불확실성 아래 | `331624f0` |
| U5 진행 기록 | native 결손은 Insufficient, 원시 식별자·실패 단계를 보존한다. 측정 중 kubectl logs follow 동시 캡처 금지. emitter 진단은 runtime 재빌드 때 반영한다 | 동시 캡처 unknown 114 vs 0. 정확 소실 단계는 미확정이나 이후 15회 무재발로 종결 | `cf92a845` |
| U6 발행 충실도 | JSON 직렬화만 stdout lock 밖으로 옮기고 한 줄 write+flush는 보호한다. gap/lateness·planned/issued·결손을 기록하며 별도 writer thread/queue는 없다 | >50ms gap 3541/961435·최대 313~959ms. writer lock이 원인이라고 확정하지 않음 | — |
| E5 최종 입력 | genesis2 제외 7 Node, p95 MNV 1s/PLC 36ms/Smol 7.034s·m 29ms. R 235.890404/387.365408/1.285195 req/s, rate=α×s×R, s=.45/.1/.45 | PLC 36ms positive 4/7, orangepi κ0·jetsonx 자원 Rejected≠κ0; [E5 보고서](../reports/e5-evaluation-20261030.md) | — |
| G0·생성기 | #203 174.56 req/s·120초 planned=issued·generator VALID, PLC 14.2ms·CPU 1.60/6·throttle 0으로 비병목 확인. 최대 G/80% 추정치는 입력하지 않는다 | 고정 6배치 Smol 12.47s·94.5%는 별도. Guaranteed 2CPU/1Gi·MNV 3 shard로 lateness 0 | `7e324981`·`e8ed3cff` |
| E5 시간·품질 | 열린 고리 trace, 수신→응답 SLO·send→수신 입구·예정→send 장비 품질을 분리한다. L 2/1/75초·cap 129초·delivery 10ms·socket L+20ms, 지각으로 L 차감 금지 | lateness>50ms 비율≤1%와 trace 전부 공급 필수. 미달은 INVALID_GENERATOR·결측은 UNKNOWN | — |
| E5 분모·판정 | planned offered 전체와 첫 준비/전환/차단 요청 유지, 별도 구간 집계를 병기한다. judge는 SLO·에너지·generator·전력만 판정하며 Node 중첩·장부·quiet 재검사는 삭제했다 | 완료-only p95와 D1 필요 rank는 다른 분모다. FAIL/NOT_READY/INCOMPLETE는 기록 후 계속, 운영 오류·INVALID만 중단 | `6f6b28c3`·`158e106c` |
| E5 에너지 | Node 공백 합≤창 1%·단일≤30s면 선형 보간 J와 공백초×W범위 오차 상한, 초과는 UNKNOWN. 공유 POOL 행 합산 금지, d/비용 3s는 불변 | 긴 행과 짧은 비용 측정의 정책 분리. 오차 상한은 CI가 아님 | `198150d6` |
| E5 비교·주입 | static은 E5-3 끝 실제 구성·같은 trace. factor 6요소 12행(placement≡membership 제외, batching은 고정 cap 대조). E5-7은 활성 weight>0·직전 assigned>0·서로 다른 Node 2개, 증거 불충분 시 INVALID | 180/360/600초 Pod 삭제·cordon 삭제·uncordon 대리 실험이며 실제 네트워크 단절은 아님. 독립 반복·모든 요소 인과·점진 감소는 미평가 | `9d0a3e13`·`c6c4a80c` |
| R1~R6 | old-serving 보존·positive-route ACK·known-effect 예약 수렴·route-only handoff·교체 dwell(기본 comparisonPeriod)·batching OFF 투영 | 1회차 원시 22카드/6군집 승인. 과부하·Smol 용량은 결함이 아님 | `75bf0a4f`·`78cf4699`·`0f206bf6`·`d0cea2eb`·`9b237c1c`·`fdb7cb53`·`22296404`·`3433204f` |
| C1·C2·C3 | parent sibling exact-proof 공유·old 보존+독립 Node 순증. C3는 profile 부재 용량 한계라 코드 변경 0, 측정 등록이 해법 | C1 신규 예약 6/6 반환·Fixed 0(전 560), E5-5 error 10636→1026·p95 1.9s→226ms. BackendUnavailable 99.9%는 native deadline으로 “라우팅 공백” 일반화 반증 | `651cd021`·`0b67b252`·`048624d7`·`8639a569` |
| 회차 비교 | 2회차 #222+#223은 같은 rev104 합성, 3회차 #224 E5-5/6+#225 E5-1/7은 표적·시작 상태 복원이다. E5-6/7 시작 상태 차이를 C1 효과로 귀속하지 않는다 | 복원 routing 훼손·selector TTL 하네스 수정. E5-7 FAIL은 Smol 용량, 격리는 CAPTURED | `90c58155`·`7d4e812c`·`46ec78be`·`22fa6439`·`cbe51e67` |
| Job·Pod 수명 | 새 Run은 증거 확정 후 Background Job 삭제·실패 86400초 보존, 생성 TTL 없음. 옛 Job 1012개는 binding 의존 확인 전 보존하고 완료 Pod만 정리한다 | Pod 1049→73·표본 5개 읽기 성공을 전수로 확대하지 않음; [ADR0026](../adr/0026-finished-measurement-pods.md) | — |
| 정책 불일치 | 성공 4 trial/정책 3인 BEO는 Failed/EnergyObjectivePolicyMismatch, 임의 제외·강제 Complete 금지 | 네 번째 trial 생성 경위는 로그 부재로 미확정; [사고0005](../postmortems/0005-measurement-campaign-stuck.md) | — |
| 관측 보존 | 로컬 2h·archive 투명 조회, 완료 payload 제거·ID tombstone 유지. 미완료 pin·보존 실패 보호, 수동 파일 삭제·메모리 증설 없음 | rev92 24h 복원 heap 3.7Gi OOM→rev93 회복. 기동·긴 창 메모리 상한 인증은 아님 | `34ec34f7`·`b7ccd4a8` |
| gateway | 분리 코드 removed @e8344b20, 단일 source·T5 live 유지. 재개 조건 충족 시 최소 설계부터 시작한다 | G0 비병목; [ADR0029](../adr/0029-gateway-control-data-plane-split.md). 과거 UID selector 성능 근거는 [사고0004](../postmortems/0004-gateway-pod-list-regression.md) | `e8344b20` |
| 증거 API·publication | CHILL 사실은 API-only, raw는 하네스 전용·Dashboard는 가공 조회. resources/cluster·meters/requestTimeout/podName 투영, PUT에서 읽기 전용 필드를 제거한다. publication은 불변이며 coverage·오차·규칙을 기록한다 | [ADR0030](../adr/0030-observation-evidence-api-streams.md), raw·과거 조회는 측정 창 밖. 외부 전력·client·STOP·운영자 명령은 유지 | `8c6bef14`·`c355e3cd`·`2d18dcee`·`85a9cab3`·`d39c9576`·`f75596f6` |
| Dashboard | WebSocket 첫 메시지 인증·폴링 없음. 개요→POOL→인스턴스/Node/예약→근거. pending은 배정 전·waiting은 배정 후로 합산 금지. 비율 분모는 completed+failed, 0이면 비율 없음 | DB-FR-36~52·[설계](../design/dashboard/README.md). 기록 재생은 별도 deferred, 서버 판정을 다시 계산하지 않음 | `afbf531b`·`9e98194a`·`d65a18ee` |
| 구성 소유권 | A/B만 기존 settings·CR·annotation·binding, C는 상수·D는 기존 설정 유지. BEO 단일 CI·startup 3필드·capacity 3설정, profileConfidenceLevel 삭제 | [ADR0031](../adr/0031-configuration-ownership-and-freezing.md). 과거 부재를 소급 기본값으로 채우지 않으며 ?10·역사 script 삭제는 미승인 | `58348057`…`30e3a6d6` |
| 저장 계약 | 옛 Run duration tolerance 0이고 spec nil이면 기본 .5 비교로 기존 profile을 무효화하지 않는다. 명시 불일치는 거부한다 | rev112 profiles/options/κ0 회귀; [ADR0019](../adr/0019-durable-stored-contract-compatibility.md) | `75127da9` |
| 운영·재현 | main 통합·spec 전체 추적·push 승인. 실험 코드는 hack/에서 관리하고 환경 인자를 분리하며 원시·비밀은 외부 보관. 측정 창 build/test 금지, live 변경은 한 명이 직렬 수행한다 | [사용자 의도](user-intent.md). 승인·배포·검증·원격 실행 증거는 각각 구분 | — |

## 최근 30일 로그 (2026-10-01~30)

| 날짜 | 최종 결정·결과와 근거 |
|---|---|
| 10-23~24 | d 일반화·signed raw/정책 투영·B-only 확정. 같은 승인 조건 대체·재시도 최대 1회 허용, 새 조건·runtime digest·범위 확대는 별도 승인 |
| 10-25 | 첫 구성→확장→축소 시연 우선, 최소 수요·Ready 부분 공급 채택. 시연·진단은 연구 평가와 분리 |
| 10-26 | 장부·예약·leximin·두 박자·H600초·fresh 수동 승인·Job 수명 채택. d 최종 유휴 기준은 식별+≤5%([ADR0028](../adr/0028-identified-idle-acceptance.md)) |
| 10-27 | d 34/34, U3 실증·U4 공유 거절·U5 무재발 종결. E5 최종 7 Node/R/SLO 승인·열린 고리 G0 비병목·품질 정책 확정. genesis2 Xid79 반복 제외, 하드웨어 원인은 추정 |
| 10-28 | E5 에너지 1%/30s·단계 FAIL 비치명·factor 6요소 12행 확정. 원시 직접 검증·코드 위치 확정으로 분석 재수행, r1 카드 폐기. gateway 분리 보류 |
| 10-29 | R1~R6 구현·R4b 보상 추가, 2회차 하네스·환경 중단. 백업 tar 4개 약31GB 삭제 승인·증거 #213/#217 이후 보존. #222 잔여는 새 재개 장치 없이 기존 pre-state 복원 #223으로 보완, 디스크 310GB 미만 정지 |
| 10-30 | #222+#223 결과 확정·사이클2 C1/C2 승인·구현 후 표적 #224/#225 완료. E5-3 측정 row 부재 용량·BackendUnavailable 포화 해석으로 정정. 보고서 `960d5308`·spec `cbe51e67` |
| 10-30 | 순서: 환경 절차→미사용 분리 코드 삭제→증거 API→S1/S2. 환경 `94a32e9a`, 분리 삭제 `e8344b20`. 외부 CloudCore Helm drift·디스크·PodGC는 문서와 live 적용을 구분 |
| 10-30 | rev106~110 배포·리허설, 옛/API KPI 동일. CPU 몫 live 2.5%/수신 7.3%/raw 1.1%/Dashboard 16.8%, agent rasp5 0.24%/nano 4.5% of core. jetsonx identity·orin-nano 수집0 미완, limiter·예약 재발 미해결 |
| 10-30 | 하네스 API-only `d39c9576`, collector GET RBAC `f75596f6`, NodePort31300 상시 공개 승인 `a7ba73cd`. 하드코딩 7커밋 반영·옛 Run 해석 `75127da9` 수정 |
| 10-30 | rev114에서 #213/#216/#222+#223/3회차 첫 게시 4건·KPI 46/46 일치. 브라우저 PASS8/FAIL0/미확인5, 전체 수용 완료 아님. [계획의 잔여·근거](../plans/2026-10-26-dashboard-live-stage.md) |

## 미해결과 원시 근거

남은 post-E5 작업은 [로드맵](../plans/2026-10-26-remaining-roadmap.md), Dashboard 재생·브라우저 미확인은 [라이브 무대](../plans/2026-10-26-dashboard-live-stage.md), 설정 ?10/삭제 미승인은 [하드코딩 요약](../plans/2026-10-30-configuration-hardcoding.md)으로 모은다.

원시: `/home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/`의 checkpoints.md·결정/제안 원문과 같은 루트 `audit-20261024/`, `campaign/v2/`, `perf/`, `deployB/`. 원시 immutable 상태·실패·결손을 현재 기본값이나 후속 성공으로 덮어쓰지 않는다.

## 2026-10-30 — 스케줄러 성능 최적화 사이클 착수(사용자 위임)

- 사용자 결정: 갭 G1(증가 구간 MNV 공급: rate-limiter 근본 원인 → 빈 Node MNV 프로파일링) → G2(Smol 전환 꼬리) → G3(에너지 Δ) → G4(factor 재실험) 순. **배포·표적 재실행·프로파일링 캠페인은 사전 승인("모두 알아서 승인, 조치 후 보고")**, 방법 제한 없음. 불변: runtime 7 이미지, 측정 재시도 금지, raw 행 판정, 증거 조작 금지, 디스크 guard.
- 사용자 지침(설계 원칙): 단편 패치(A→A') 금지. 큰 그림에서 구조적 원인을 찾아 쉽고 가볍고 널리 알려진 패턴을 응용해 근본적으로 해결. 각 수정은 구조적 원인·적용 패턴·tradeoff와 재발 방지 invariant를 카드에 기록.

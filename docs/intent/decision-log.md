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
- 사용자 지침(검증): 해결 방향은 구현 전 타사 모델 패널(fusion: 3 패널 독립 + 판정자) 리뷰를 거쳐 확정. 워크플로우 2단계(본질·설계)와 3단계(구현) 사이에 "설계 브리프 → 패널 리뷰 → 방향 확정" 게이트 추가.

## 2026-10-30 — 최적화 사이클 1~3 1차 결과(rev118)

- 패널 판정(`.fusion` 기록, local judge-verdict-cycle123): 브리프2(jetsonx 모드 투영) → 브리프1(예약 증명 경계) → 브리프3(D2 terminal tombstone → D1 staged handoff, veto 삭제 금지).
- 커밋: `d788bd11`(AtMode 정규화·모든 가상 writer 통일), `cde9e4de`/`03193e1e`/`bdce22f0`(owner baseline·route receipt·P1), `532f3240`(Terminated exact tombstone), 선행 `8e54818b`/`1e63512b`. 사이클 1 결론: rate limiter는 E5 원인 아님(E5-3/5 raw limiter 0행; rehearsal 취소는 autonomy-off 정상 취소).
- rev118(서버 532f3240): jetsonx MNV7·PLC6 rows CapacityRejected→Built, **R MNV 235.89→300.61, Smol 1.285→1.651**(PLC 불변). genesis3 GPU_1500 occupancy 1회 Accepted(6.578ms/req) → MNV b1~b8 κ≈68~72 Built. 두 번째 replay R(302.6/316.4/0.83)은 genesis3가 GPU_1500 상태에서 찍힌 snapshot 영향 — fixed 복귀 후 재replay 필요(수치 채택 금지).
- 운영 주의: 다른 세션이 rev117(소스 04053ec) 배포함 — 두 세션 동시 클러스터 조작 위험. 증거 디렉터리 충돌로 일부 배포 증거 덮어쓰기(control-236, 재구성 기록).
- 다음: 4회차 표적 재실행(E5-3/E5-5/E5-7, 3회차와 동일 rate·seed) → 비교; D1 커밋 후 재배포·재실행.

## 2026-10-31 — 4회차 표적(#237, rev120 서버 a3d020d6) 결과: 회귀

- 3회차→4회차(동일 rate/seed): E5-3 MNV 실패 13,563→**44,291**(p95 1,655→1,964ms, INVALID_HARNESS: 창 종료 후 static-freeze STOP — 측정창 자체는 완주); E5-5 MNV 실패 1,059→**12,953**(D1 FAIL), Smol p95 14,093→8,947ms(FAIL 유지); E5-7 Smol 실패 114→0이나 p95 13,173ms(FAIL), MNV p95 94→166ms. E5-5 후 실패 prepare 예약 잔류(>600s, owner plan-594d2726df2f) → takeover 1회.
- 판단: rev118/120 변경(jetsonx 후보 편입·예약 증명·Terminated)이 **SLO를 악화**시킴. D1(`4b2b4bb0`,`6c268842`) 배포 보류. 사이클 5 = 4회차 raw 기반 결함 추출(bottom-up) 후 패널 리뷰 → 수정.
- 경로: `control-222-full/full-E5-{3,5,7}-4/`, `tables-4.txt`, `comparison-4-vs-3.{txt,json}`, `O/deployB/control-237-20261030/targeted-r4/`.
- 사이클 5 추출(카드 `A/cards-r5/`): E5-3 — α=.5 말 정상 ACK된 고κ 구성(jetsonx CUDA+orangepi q4, κ99.6)을 CPU 조합(κ64.3)으로 교체 후 DemandIncreasing/ScaleDownStabilizing이 복귀 차단 → α=.9 포화 실패 36k; E5-5 — CUDA 공급 감소 후 CPU 단독 4분(12:49–12:53) 실패 집중, 예약 잔류 1,353s(신규 Pod ACK로 R4b 제외 + 타 Node 기존 source 증명 receipt 부재 → 반환 불가); E5-7 — D2 복구 확인(Smol 새 dispatch 65s, MNV 9.5s/39s), p95는 복구 공백·큐. jetsonx 편입 자체는 정상 동작(CPU 충돌 0).
- 구조적 쟁점: (a) 축소 즉시·확장 지연의 **tempo 비대칭**(에너지 목적이 κ 감소 교체를 택하고 stabilizing gate가 복귀를 막음), (b) 예약 반환 증명이 "변경 없는 기존 executor"를 증명할 수단 없음. 둘 다 브리프→패널 게이트.
- 사이클 5 패널 판정(`local judge-verdict-cycle5`): B ReturnProof(`a3553ccd`,`2516ec0c`: admission baseline+단조 mayTouch, untouched proof) → A Tempo(`855dbe9c`: `scaleDownHeadroomFraction` 기본 0, UID별 증명된 회복의 timing 예외; growing/partial 분리). D1 staged handoff(`4b2b4bb0`,`6c268842`) 포함. gate PASS. **rev121 배포 + 5회차 표적(E5-3/5/7)**, h=.25는 이번 라운드 live ConfigMap 값(binding 기록; 배포 안전값 아님 — 대조 실험값).

## 2026-10-31 — 5회차 표적(rev124 서버 2516ec0c, h=.25) 결과 + 외부 개입

- E5-3(유효): MNV 실패 3회차 13,563 → 4회차 44,291 → **5회차 9,511**(p95 1,655→1,964→1,496ms), 에너지 Δ5−3 −6,836J(CHILL 소비 증가 방향·headroom 비용), D1 FAIL 유지. 사이클 5(ReturnProof+Tempo headroom)가 4회차 회귀를 되돌리고 3회차보다 개선.
- E5-5: INVALID_HARNESS(상위 명령 3600s clamp 종료) + **타 세션이 측정 중 rev125(소스 27b32273, main 아님) 재배포** → API 거부·수집 누락. E5-7 미실행(live 소스 불일치). producer 3개·serving 잔류 — 정리 필요.
- 운영 결정 필요(사용자): 두 세션 동시 클러스터 조작 금지 — 배포·측정 권한 단일화.
- 경로: `control-222-full/full-E5-{3,5}-5/`, `tables-5.txt`, `comparison-5-vs-{3,4}.*`, `O/deployB/control-246-20261030/targeted-r5/report-244.txt`.
- 에너지 카드(`cards-r5/energy-01.md`, verify 146/146): 5회차 E5-3은 **−6,836J(vs 2회차 재사용 E5-3) 및 고정 대비 +5,197J 절감**과 실패 −4,052 동시 달성(부호 교정: 앞선 "−6,836J 소비 증가" 기술은 오류). **단, 5회차 tick의 `scaleDownHeadroomFraction=0`** — 표적 복원이 3회차 binding settings를 동결해 PUT h=.25가 적용되지 않음 → 5회차 개선은 ReturnProof+회복 timing 예외+D1의 효과이며 h 효과는 미측정. 다음 라운드는 binding settings에 h 명시.
- 에너지 구조 후보: ① zero-route 준비 인스턴스 장기 잔류(2회차 E5-6 3 Node × 3,600s, Node wall 57.6kJ 노출; E5-5 1,085s), ② 빈 Node(jetsonx) 모드 하향 미실행(E5-6 1시간 9.3W), ③ 전환 비용 회수기간, ④ h frontier. 정적 기준선 κ 119.6→91.9가 2회차 Δ 변화의 70.6%(−20.7kJ) 혼입.
- 에너지 브리프(`local brief-BriefEnergy`): ① E5-6 zero-route 잔류는 361 tick 전부 ReservedScope/ReservedNode(owned UID Fixed) → idle 회수 차단 = 예약 잔류 계열(사이클 5 ReturnProof가 대상); ② jetsonx 빈 Node는 실측 baseline상 MODE_20W_2CORE가 이미 최저라 하향 후보 없음 → 더 낮은 모드 idle baseline 측정이 선행(코드 아님). 사이클 4 코드 변경 없음; E5-6 재실행으로 ReturnProof 효과 확인 예정.
- 사용자 결정(2026-10-31): **배포·측정 권한은 이 세션(ops 에이전트)으로 단일화**. 다른 세션은 커밋만, 배포는 요청 경유. 조치: 잔류 정리 → main `2516ec0c` 복귀(rev126) → E5-5/E5-7(h=.25를 binding settings에 명시)·E5-6 재실행.
- 사용자 요청(2026-10-31): 대시보드 HTTP NodePort 31300 제거(라이브 보드 WebSocket Origin 검사 https 강제 → 403 "연결 끊김"; HTTP Origin 허용은 토큰 평문 노출이라 불가). 조치: live values `dashboard.service.type=ClusterIP`, nodePort 제거(차트만, 이미지 불변), 측정 창 밖에서 적용. 안내 URL https://155.230.35.213:31104/ 통일.

## 2026-10-31 — 6회차 표적(rev127 서버 2516ec0c, h=.25) 결과

- E5-7(유효): Smol 실패 114→**2**(D2 복구 작동), p95 1,256→5,671ms(FAIL 유지); MNV p95 94→199ms; Δ6−3 에너지 +6,667J.
- E5-6(유효): MNV 실패 100→**0**, p95 305→196ms; **PLC 실패 0→413**(신규 회귀, 추출 필요); Smol p95 1,962→2,963ms; 에너지 +7,758J(3회차 351.3kJ→359.1kJ, 고정 367.8kJ 대비 여전히 절감 +8.7kJ) — headroom 비용.
- E5-5: INVALID_CONFIGURATION(복원이 h=0으로 되돌림). 참고치: MNV 실패 1,059→14,467(h=0에서 4회차와 동급), Smol p95 14,093→3,625(D1 충족). h=.25 유효 재실행 필요.
- 대시보드 NodePort 31300 제거 완료(ClusterIP, https://155.230.35.213:31104/ 200).
- 다음: E5-5 h=.25 유효 재실행 1회 + 6회차 raw 결함 추출(PLC 413, Smol E5-7 꼬리, MNV p95 상승, 에너지 귀속).

## 2026-10-31 — 사이클 6 + h 미적용 근본 원인

- E5-5 6b(h=.25 의도): MNV 실패 1,059→11,879, Smol D1 충족(p95 3,749ms), 에너지 −4,588J. 그러나 raw: stage 시작 직전 하네스의 full-settings PUT(S20:5425→5446)이 h를 0으로 되돌림 → **5/6/6b회차 모두 h=0으로 실행**. 근본: 표적 복원이 3회차 pre-settings 전체를 PUT(정책 필드 포함). 수정 `ea47d4ce`: 복원은 토폴로지 제어 키(autonomousWorkloads/excludedNodes/controls)만 pre-state에서, 나머지는 live 정책 유지(self-check 단언).
- 사이클 6 패널 판정(`local judge-verdict-cycle6`): A EmptyRoute(`ebf7c0bb`,`3f79378d`,`81f1d3df`,`6efea38b`,`4d0377bc`: 무Ready UID 생략·scope 교집합·양수→empty 전이 거절·explicit unroute), B RecoveryDAG(`dc287bd0`,`8ebe8f33`: Terminated 항목 growing 보호 제외·완료 신호로 frozen DAG 전진, 탐색 없음). gate PASS(HEAD 79438382).
- 7회차: 서버 79438382 + h=.25(첫 유효 적용) E5-3/5/6/7.
- 7회차 준비 중 발견·수정: (1) κ replay "hang"은 제품 회귀 아님 — κ argv에서 `-workloads/-margin` 누락(기본 all 워크로드) → 장시간 replay; #236 argv로 교정. (2) 다른 세션의 profiles API 변경(decorateProfiles가 `preparations` 삭제 + 기본 25행 paging)이 하네스 복원 계약(SCH-67)을 깨 E5-3 STOP → `d4a933bf`: 기본 응답은 전체 catalog(limit/offset 시에만 paging), `preparations` 유지, raw 표본(measurementFacts/durationNanos)만 생략. 교훈: 공유 API는 계약 테스트가 소비자(하네스) 쪽에 있어야 함 — e5 self-check에 profiles 응답 키 검증 추가 후보.

## 2026-10-31 — 7회차(서버 d4a933bf = 사이클 1~6 전부, h=.25 **첫 유효 적용**) 결과

| 행 | 지표 | 3회차 → 7회차 |
|---|---|---|
| E5-3 | MNV 실패 / p95 | 13,563 → **1,345** / 1,655 → **256ms**; D1 2/3 → **3/3**; 전력 220,082 → 213,905J(−6.2kJ) |
| E5-5 | MNV 실패 / p95 | 1,059 → 5,264 / 226 → 364ms; Smol p95 14,093 → **3,629ms**(D1 충족); PLC 실패 0→7; D1 2/3; 전력 141,732 → 138,004J |
| E5-7 | Smol 실패 / p95 | 114 → **1** / 1,256 → 2,638ms; MNV p95 94→184ms; D1 2/3 → **3/3**; 전력 +7.4kJ |
| E5-6 | — | 복원 전 예약 잔류(7)로 STOP, 미측정 |

- 잔류 예약: E5-3 뒤 1개, E5-7 뒤 7개 — 사이클 7 대상(원형 `control-258-20261031/reservation-leftover-*`).
- 남은 갭: E5-5 MNV(재급증 회복, 5,264), E5-7 MNV p95 상승(jetsonx CPU queue), 예약 잔류, E5-7 전력 증가.
- 사이클 7 추출(`cards-r7/`): ① E5-7 Smol 복구 85.3→**42.3s**(완료 신호 전진 효과: action 간 대기 32.8→3.4s; cold prepare 26s 잔존). ② 예약 잔류 2유형 — (a) 늦게 Ready/ACK한 orangepi Pod의 exact UID/container가 prepare 효과 신원에 미등록 → 반환·R4b 불가(≥750s); (b) `changedWorkloads=[]`(무Ready UID 전부 생략)인데 부모 예약은 생성되고 child 없음 → 영구 잔류(7개, ≥910~1,550s) = 사이클 6 A의 회귀. ③ E5-5: h=.25 유효(120/120 tick), 저κ 40.4 차단하나 47.8→47.8 교체 허용; 복원은 fast 경로 24s(prepare 03.16→Ready 12.42→route 27.24)로 빨라졌지만 3배 재급증 첫 60s에 BU 5,106 — 과거 저부하 기준 headroom의 구조적 한계. ④ MNV p95 184ms tail의 65.5%는 nano(jetsonx 11.9%).
- 사이클 7 패널(2인; Gemini quota 소진) 판정(`local judge-verdict-cycle7`): A `1f8e8ca3`(빈 DAG 부모 미생성·no-effect 반환·prepare UID 증거·동일 UID late binding·결정적 blocker reason), B `60009f0f`(퇴역 진입 직전 최신 수요로 guarded commit·Continue에도 수요 관측·미시작 suffix no-effect 중단; raw: 수요 95.1 감지 0.245s 뒤 final route→24s 뒤 stop이 원인). minimumDemand 상향은 별도 설정 실험(보류). gate PASS → 8회차(E5-5/7/6 + E5-3 회귀 확인).
- 8회차 1차 시도(서버 60009f0f): 재시작 후 **orphan 예약 7개 자동 반환 확인**(사이클 7 A 효과). 그러나 E5-5 중 서버 panic 3회(`SchedulerReductionWorkloads`가 빈 planner로 `kappa(…,0)` 호출 → index out of range; resize 대상에서만 발생, 단위 테스트에 resize 시나리오 부재). 수정 `b620a315`(rows에서 직접 κ 조회, resize 회귀 추가). 교훈: 공유 프로세스(추론 경로 포함)에서 planner panic이 서버를 죽임 — 제어 루프 fail-safe(recover→tick 실패 공개)는 다음 패널 안건.
- 운영 실수(Main): 디스크 정리 중 `O/harness/e5-*` 동결 하네스 사본(최근 3개 외)을 삭제 → 8b E5-5가 `e5-5f1dc4f1` 참조 실패·archive 수집 불완전 STOP. `git archive <sha>`로 복원(a3d020d6, d45fe66e, 947b50a2, 134f56bb, 5f1dc4f1). 규칙 추가: `O/harness/`는 binding이 참조하는 한 삭제 금지(ops 가이드 반영 필요).

## 2026-10-31 — 8d(서버 b620a315 = 사이클 7) E5-5 결과: 심각 회귀

- E5-5: MNV 실패 **50,994**(7회차 5,264), p95 1,972ms; PLC/Smol 정상(Smol p95 3,820). 사용자 지시로 8d 중단(E5-7/6/3 미측정). 전력 136,195J.
- 조치: 측정 중단, 워크플로우 전환 — 오프라인 결정 리플레이(`hack/kappa -mode compare`) 1차 + 미니 행(`36c8778c`) 2차, 원클릭 러너(`a0b0422f`). dashboard만 HEAD 36c8778c 배포(조언 세션 요청).

## 2026-10-31 — 워크플로우 전환: 오프라인 리플레이 게이트가 배포 전 필수

- 8d 회귀 원인(리플레이로 확정, 카드 `cards-r8/full-E5-5-8d-01.md` 일치): 사이클 7 B의 `SchedulerReductionCommit`이 **owner 단위**로 거부 — 혼합 target(MNV 회복 103.7↑ + PLC 축소 218→77)을 `ScaleDownWindowCapacityInsufficient`로 통째 거부 → 준비된 인스턴스 route 없이 방치, BU 50,000. 조치: `60009f0f`·`b620a315` revert(`d2831eff`,`e68e1d43`), 사이클 7 A(`1f8e8ca3`)는 유지.
- 게이트(`79f5fd7f`, `hack/e5/replay-gate.py`+`kappa -mode guard`): 기록 tick(7회차 E5-3/5/7, 8d)을 후보 바이너리로 재결정·guard 재검증, 기준 소스(live)의 bad tick과 `(at, recorded targetID)`로 대응해 후보에만 있는 bad tick이 있으면 FAIL(`newBadTicks` 목록·`fixedTicks` 수 표시, 다른 tick 개선으로 상쇄 금지). 실행 30s/stage. 결과: b620a315 FAIL(6~13 거부), revert HEAD PASS(기준 d4a933bf와 동일). 증거 `O/campaign/v2/replay-gate/2026-10-31*/`.
- 규칙: 코드 변경 → 게이트 PASS → 미니 행(`36c8778c`) → 정본 전체 행은 릴리스 후보만. 원클릭 러너 `a0b0422f`.
- 사이클 7 B 재설계는 "UID별 guard(해당 UID 공급이 줄 때만, 회복 UID 미차단)"로 다시 — 게이트에 8d 기록이 있으므로 오프라인에서 검증 가능.

## 2026-10-31 — 사용자 의도 재확인(강제 6항)

1. 리뷰 복구: 사이클 3~7 미리뷰 scheduler 커밋(a3553ccd 855dbe9c dc287bd0 1f8e8ca3 등) 정식+ponytail 리뷰 1회 묶음. 이후 모든 구현: **리뷰 2종 PASS → 리플레이 게이트 PASS → 배포**(하나라도 빠지면 배포 금지).
2. 덧대기 중단: 새 가드·증명·예외 추가 금지. 다음 사이클은 사이클 3~7 덧댐 전체를 한 장 그림으로 정리 → 알려진 단일 모델(k8s rolling update maxSurge/maxUnavailable+readiness, level-triggered reconcile)로 대체 가능성 판단 → 가능하면 덧댄 코드 삭제. 패널은 방향+최종 diff 검토.
3. 점수판 고정: 고정 벤치마크(같은 시작 상태·설정·행 = E5-3/5/7 미니 행 + 리플레이 입력). 점수 = ① 전 행 SLO 실패 수 ② 에너지. 기준 버전 = 7회차 d4a933bf.
4. 측정 전 설정 gate: 첫 tick 실제 설정(h)·서버 digest·외부 배포 없음 확인, 불일치 시 시작 안 함; 측정 창 타 세션 배포 금지 방송.
5. 멈춤: 같은 원인 2회 → 즉시 정지+본질 1줄. 운영은 round.py 한 명령만(라운드마다 새 에이전트 금지).
6. 보고: 단계 전환마다 3줄(지금 어디·막힌 것과 본질·다음 하나); 30분 조용하면 진행 1줄.
순서: revert HEAD 게이트 PASS 확인 후 배포 → 1) 리뷰 → 3) 기준 측정 1회(미니 행) → 2) 구조 검토.
- 사용자 강제(2026-10-31): **구현 = 최소 커밋 서브에이전트 + 정식 리뷰 + ponytail 리뷰**. 세 가지 모두 거친 커밋만 통합·게이트·배포 대상. Main은 지휘·근거 확인·통합만.

## 최적화 국면 워크플로우(고정, 10/30 09:45~)

0 점수판(미니 행+리플레이, 점수=SLO 실패→에너지, 기준 d4a933bf) → 1 raw 추출 카드 → 2 본질(덧대기 금지; 사이클 3~7 덧댐 한 장 그림→단일 모델 대체→삭제 방향) → 3 패널 3+판정(방향+최종 diff) → 4 구현(최소 커밋 서브에이전트+정식 리뷰+ponytail 리뷰) → 5 게이트(lint/test/arch + 리플레이 게이트 PASS) → 6 round.py 한 명령(측정 전 설정/digest/외부배포 gate, 측정 창 방송, 미니 행). 정지: 같은 원인 2회. 보고: 단계마다 3줄, 30분 침묵 금지.

## 2026-10-31 — 리뷰 복구 결과(사이클 1~7 scheduler 커밋 19개)

- 정식 리뷰 BLOCK: F1(P1) `4b2b4bb0` kube/scheduler.go:571 — 필수 bridge route 실패가 부모 실패로 전파되지 않아 예약 영구 running; F2(P1) `ebf7c0bb`/`1f8e8ca3` scheduler.go:1051 — ready-subset이 선행 ACK하면 필수 bridge가 빈 scope로 생략되고 blocker stop이 존재하지 않는 run을 영원히 대기; F3(P2) `6c268842` scheduler_bridge.go:408 — 보존 UID의 Terminated route가 bridge Ready 검사에 포함되어 복구 후보 상실; `8ebe8f33` late-ack 테스트 미검증. 나머지 15개 통과.
- ponytail 수정요구 5건(−34줄): returnProofParent/frozenActionParent 재탐색 삭제, releaseObservedScopes terminal 중복, ApplySchedulerWorkloadsTarget UID 중복 검사, scheduledOperation RouteTarget 재선택, operations.go 불가능 fallback. 전환·예약 가드 전체 표는 `agent://ReviewPonytail`(2단계 입력).
- 조치: F1/F2/F3은 **새 가드 없이** 기존 실패 전파·handoff 소비 경계 수정(덧대기 금지 준수) → 최소 커밋 서브에이전트 → 리뷰 2종 → 리플레이 게이트. 배포 중인 HEAD는 7회차와 동일 코드(기준)이므로 유지.
- 배포(control-266): 서버만 `4626dc8c`(= 7회차 d4a933bf 동작 + 사이클 7 A + 리플레이 게이트). round.py는 prebuilt publication 전제라 publication 생성은 기존 fast-deploy 경로(빌드+publish)로 Main이 직접 수행; 이후 라운드는 round.py가 그 publication을 소비. 관찰: ops 에이전트 2회 연속 환경/전제 누락 STOP → 규칙 5에 따라 Main 직접 실행.

## 2026-10-31 — 리뷰 복구 수정 배포(control-267 dashboard, control-268 server = b0877c1e)

- 수정 체인(모두 최소 커밋 + 정식·ponytail 리뷰 PASS + 리플레이 게이트 PASS(기준 4626dc8c) + clean Go gate): F1 `7370665d`, F2 `70d1bd76`+`41275915`, F3 `19162c20`, 다단 bridge `922ff34d`+`0e171b06`+`b0877c1e`, late-ack 회귀 `694f6ed0`, StartedAt `1e9aaf7e`(조언 세션), ponytail 삭제 `9ebc5f17`(−26)·`3a6c5e91`(−27). 새 가드·예외 없음(리뷰 확인).
- live: server·dashboard 모두 `b0877c1e`, restart 0. 07:10Z부터 20~30분 클러스터 무변경(조언 세션 재설문).
- 다음: 점수판 정의 → 기준 측정 1회(미니 행, round.py) → 2단계 구조 검토(ponytail 가드 표 입력).

## 2026-10-31 — 2단계 구조 판정(패널 3+판정, `local judge-verdict-structure`)

- 채택 모델: **P/R/L 3층 = 인가된 경로를 관측으로 전진시키는 level-triggered rollout** (P Planner: κ·weight·전환 비용·에너지로 목표+경로; R Rollout reconciler: tick·completion 공통 전진; L Effect responsibility: 인가된 쓰기·미확정 실물 책임). Deployment/Argo 도입 아님(κ≠replica, physicalBudget≠maxSurge, ACK·drain 부재).
- 기각: TTL, 실행 종료 즉시 반환, 단순 orphan GC, 반환 증명 원장 일괄 삭제(코드 확인: ownerReturnTerminal·knownEffectConfirmed·late binding·preparationCreatedSource가 각기 다른 사실을 지킴).
- 삭제 단계: S0 기준·예약 반사실 진단(도구만) → S1 tick/completion 중복 제거(scheduler_continuation 60줄 범위) → S2 route/퇴역 판단 통합(readySubset·dependenciesComplete 147줄) → S3 no-effect 부모 수명 제거(87줄) → S4 cleanup route 우회로 제거(161줄). 교체 범위 455줄, 순삭제 미확정.
- 검증: 결정 리플레이는 실행부를 검증하지 못함 → 단계마다 리뷰 2종 → 사건 순서 단위 테스트 → 결정 게이트(계획 불변) → 고정 미니 행 점수판.

## 2026-10-31 — 점수판 기준 측정(round-10, 서버 d4a933bf, h=.25, 미니 행)

- **기준 점수 (F, J) = (7,782, 153,056.668 J)**. E5-3m MNV 실패 5,787 D1 FAIL / E5-5m MNV 1,992 D1 FAIL / E5-7m Smol D1 FAIL(실패 0); PLC·나머지 정상. 전력 complete 3/3. 측정 재시도 0. 증거 `C/full-E5-{3m,5m,7m}-10/`, `O/deployB/round-10-baseline/`.
- STOP 7: finish의 owned reset 대기 시간 초과(무ACK/잔류 예약) — 8c/8d와 같은 원인(반복 2회↑) = L층 책임 수렴 문제, S1~S4 범위. 측정 데이터는 유효.

## 2026-10-31 — S1 완료(배포 대기)

- S1 `67fe02aa`(Tick/Continue 공통 captureReconciled, 제품 −4줄) + 테스트 정리 `5644b165`(freshness-before-reconcile 단언 fail-before 확인) + lint `1bc385d4`. 정식·ponytail 리뷰 PASS(2회), 패널 2인 diff 검토 "목표 달성·의미 동일·덧댐 없음". 리플레이 게이트(per-tick, 기준 live b0877c1e) PASS, make test PASS.
- 차단 1건(타 세션): `internal/serving/kube/measurement_projection.go:74 projectOperationStep` gocyclo 31 — 다른 세션이 커밋한 코드로 clean `make lint` 실패. Main은 수정 안 함(소유권). 다른 세션 수정 후 S1 배포·미니 행 측정.
- live: server b0877c1e(control-270), 잔류 예약 0.
- 09:09Z control-271: server·dashboard = `288153fd`(조언 세션 p90·커버리지 변경 fd8bde4c + S1 + lint 분리 `288153fd`). 게이트: 리플레이 PASS(기준 b0877c1e, new bad tick 0), clean lint/test/arch/helm PASS, 리뷰 2종 PASS. 09:09Z~09:39Z 클러스터 무변경(조언 세션 재설문). S2 패널 3인 완료(범위 축소 권고: dependenciesComplete 증거·범위 결정 단일화만, readySubset 본문 유지).

## 2026-10-31 — S2 진행

- `245a88b5` S2: dependenciesComplete 소비 범위를 `handoffScope` 한 곳에서 결정하고, 증거 판정을 3갈래로 나눔. 엄격화: 완료되지 않은 dependency run은 별도 ACK로 소비 불가(fail-before 확인). 리뷰 2종 PASS, 패널 2인 "목표 충족·회귀 없음". 패널 1 축소안 적용 예정(순증 +10 → 순감).
- 미해결 관찰(S2 회귀 아님, 추후 판단) [추정]: subset ACK 뒤 prepare 결과 전에 tick이 하나 더 있으면 run==nil 경로의 stop이 frozen bridge 자신의 prepare 의존을 보지 않고 출발할 수 있음. target_scope.go:173 부모 범위는 S3에서 다룸.
- 09:40Z round-11(S1, 288153fd) 측정 창. 디스크 guard: control-213/214/216 대형 json을 gzip으로 무손실 압축 → 335GB.

## 2026-10-31 — round-11 점수(S1 포함 live 288153fd) vs 기준

- (F, J): 기준 d4a933bf (7,782, 153,057 J) → 288153fd **(6,099, 152,560 J)**. 사전식으로 개선(F −1,683, −22%). MNV 실패: E5-3m 5,787→4,407, E5-5m 1,992→1,689. E5-7m Smol D1 FAIL은 그대로(실패 0). PLC 정상. 전력 complete 3/3. 측정 재시도 0.
- 귀속: 288153fd = 리뷰 복구(b0877c1e 계열) + S1. S1 자체는 의미 불변 리팩터이므로 개선은 주로 리뷰 복구분으로 봄 [추정]. 행당 1회 시행이라 잡음 폭은 미측정.
- STOP 7(최종 상태): round 자신의 cleanup `manual-reset:9742040b` 예약이 확인 시점에 아직 잡혀 있었음. 직후 live는 instances 0·예약 0. round-10의 STOP 7(plan 소유자 잔류)과는 원인이 다름. 첫 발생이라 기록만 함. 측정 데이터는 유효.

## 2026-10-31 — S2 게이트 통과, S2b·S3 방향 결정

- S2 `245a88b5`+`2ea63c10`: 제품 순감 4줄. 리뷰 2종 PASS(2회). 리플레이 게이트 PASS(기준 live 288153fd, new bad tick 0). clean lint/test/arch/helm PASS.
- S3 패널(2인, Gemini는 quota 소진): 표식(noEffectRoutes)과 partial 특례를 삭제하는 방향은 맞음. 다만 생략 bridge의 완료를 현재 scope로 판정하면 안 됨(stage가 진행되면 다시 관측할 수 없어 부모가 영구 running이 되고, 오래된 bridge가 재실행됨). 대신 frozen edge와 receipt로 판정: 의존 action의 run이 complete면 소비된 것으로 봄. ACK가 없는 retirement-only 선언은 admission에서 제외해 영구 running을 막음.
- 8c는 실재하는 R 문제로 판정. 생략 bridge의 자기 의존 재귀 검사는 덧댐이 아니라 P 의존 계약의 복원임(2/2). S3보다 먼저 별도 S2b로 진행.
- 배포 계획: S2+S2b를 한 번에 배포하고 round-12로 측정.

## 2026-10-31 — S2b 배포·round-12, S3 리뷰

- S2b `d1f4b460`(+17, 생략 bridge 자기 의존 재귀; fail-before 확인): 리뷰 2종 PASS, 리플레이 PASS, clean gate PASS. 10:53Z control-272 서버 배포. 10:56Z round-12(S2+S2b) 측정 시작.
- S3 `702604c7`(제품 순삭제 70줄): 리뷰 2종 PASS, 패널 2인 조건부 승인. 후속 S3b로 처리할 항목:
  (1) stale bridge 재실행은 기존에 있던 문제. 소비 판정을 launch에도 공유해 차단.
  (2) admission이 route-only NotReady 계획을 막아 readySubset 회복이 늦어질 위험. 비어 있지 않은 route는 무조건 admission하고, retirement에만 dependenciesReady를 요구.
  (3) 문서 2곳 상충.
  잔존 [추정]: 재시작이나 GC로 완료 ACK가 사라지면 prepare→생략 bridge→stop 부모가 영구 running이 될 수 있음. S3 이전부터 있던 문제로, S4 이후 판단.

## 2026-10-31 — round-12 결과·STOP 7 반복 정지

- round-12(S2+S2b, d1f4b460): **(F, J) = (10,235, 153,684 J)**. round-11(6,099)과 기준(7,782)보다 악화. E5-3m MNV 8,162, E5-5m 2,064, PLC 6. E5-7m Smol D1 FAIL은 그대로. 측정 재시도 0. E5-7m 판정 직후 디스크 guard에 걸려 STOP했고, 비입력 judgment를 gzip으로 무손실 압축(320GB 확보)한 뒤 guard 이후 단계만 재개함.
- STOP 7(최종 상태)이 같은 원인으로 2회째: 확인 시점에 round 자신의 `manual-reset:*` 예약이 아직 남아 있었고, 직후 live는 예약 0. 멈춤 규칙에 따라 운영을 정지함. 사용자 결정에 따라 서버 쪽 원인부터 조사(ResetRelease).
- S2/S2b 악화 원인 조사 착수(Regress12). 판정이 나오기 전에는 S3/S3b(`702604c7`, `645f7226`, 합계 순삭제 66줄) 배포를 보류.
- 12:08Z control-273: dashboard만 `b93a14a5`(1b07daa4 포함; 조언 세션 리뷰 2종·tsc/eslint/test 162/162/build PASS 확인)로 배포. server는 d1f4b460 그대로, 게이트웨이 재시작 없음. 12:08Z~12:38Z 클러스터 무변경(조언 세션 재설문).
- STOP 7 서버 원인: 성공한 reset은 응답 전에 이미 예약을 원자적으로 반환함. State()가 마지막 tick의 Snapshot.Reservations 캐시를 live Actions와 함께 내보내서 생긴 불일치(`local://reset-release.md`). 수정 `b93a14a5`(State 경계에서 재투영 1줄, fail-before 확인). round-10의 STOP 7은 별개(plan owner 600s 정체, reset 미시도).

## 2026-10-31 — round-12 악화 원인 판정: S2/S2b 아님, 1회 측정의 계획 분기 잡음

- 실패 증가분의 97.7%가 E5-3m +180~360초에 몰림. round-11과 12는 +49초에 이미 다른 계획을 골랐음(11은 Keep, 12는 jetsonx CPU 조기 확장). 그 결과 180초 부하 상승 때 12는 CUDA prepare 3개 교체라는 긴 critical path(nano 131초)를 선택함. 첫 분기는 S2/S2b가 영향을 주는 구조(생략 bridge, 미완료 dependency)가 생기기 전임(`local://regress-12.md`).
- 오프라인 실행부 A/B: 같은 frozen 계획과 사건 순서를 288153fd와 d1f4b460에 넣었을 때 launch·stop 적격·예약 해제 판단이 동일함(`local://ab-12.md`).
- 입력(요청 38,299개 해시, 송신 지연)은 세 회차 동일. 따라서 **동일 입력에서도 tick 위상 차이로 계획이 분기하며, 행당 1회 측정의 F 변동이 ±40% 이상**임. 10→11(−24%)도 같은 이유로 리뷰 복구 효과라고 확정할 수 없음 [추정].
- S3+S3b+State 수정 HEAD 61987770: 리뷰 2종 PASS(각각), 리플레이 PASS(기준 d1f4b460), clean gate PASS. 12:38Z 이후 서버 배포.
- 알려진 한계(S3b 리뷰): route-only 목적지가 계속 NotReady이면 부모 예약이 유한 시간 안에 반환되지 않음. S3 이전에도 같은 동작.

## 2026-10-31 — 측정 방식 변경(Main 자율 결정, 사용자 의도 "SLO 먼저, 그다음 에너지"에 맞춤)

- 근거: 행당 1회 측정은 같은 입력에서도 계획 분기 때문에 F가 ±40% 이상 흔들려 변경의 효과를 판정할 수 없음.
- 동작 불변 리팩터(S1~S4류): 리뷰 2종 + 리플레이 게이트 + 실행부 A/B로 판정하고, 미니 행 측정은 생략함.
- 동작을 바꾸는 변경(계획·용량·지연에 영향): 기준과 후보를 교대로 행당 3회 이상 측정함. 판정은 F 중앙값과 범위로 하며, 범위가 겹치면 "차이 없음"으로 봄.
- 이어서 구조 분석 착수(FailAnatomy: 데이터, LoopStructure: 제어 루프·코드). 사용자 요청 "한발 물러서서 진짜 결함".
- 정정(조언 세션 의견 반영): round-11의 −22%는 잡음 범위 안이라 효과로 주장하지 않음. round-12 악화도 잡음으로 덮지 않음. E5-3m +180~360초 실패는 3대 교체의 final route가 가장 느린 prepare(nano CUDA 131초)까지 기다리는 동안, 먼저 Ready가 된 rasp5(+208초)와 jetsonx(+245초)를 가중치 0으로 놀린 구조 결함임. 다음 최적화 대상 후보로, 알려진 모델은 readiness 기반 점진 투입(maxUnavailable 0)임. 구조 분석으로 확인한 뒤 확정.
- 측정 방식 조정: 동작을 바꾸는 변경은 E5-3m만 기준 d4a933bf와 교대로 각 3회 측정하고 평균과 범위로 판정. 다른 행은 최종 후보에서만 측정.

## 2026-10-31 — 구조 분석 결론(`local://fail-anatomy.md`, `local://loop-structure.md`)

- 근본 원인: SLO는 계획의 합격 조건(총합 κ, 전환 W ≤ L)으로만 쓰이고 목적함수는 에너지임. 그래서 계획이 합격선에 붙는데, 합격 판정이 실제 실패 원인(장치별 native 큐 포화, 기존 대기 시간, 준비 중 공급 공백)을 보지 않음. 회차 간 "잡음"(+49초 분기, 에너지 1.26% 차이로 선택이 바뀜)도 같은 구조에서 나옴.
- 실패 실체: 502의 대부분은 느린 장치의 native queue deadline 초과(queue p50 1.95초, service 40~50ms). 빠른 orangepi는 p95 90ms로 여유가 있었음. 무경로가 아님.
- 결함 1: 합격 판정이 현실과 다름(장치별 배분이 감당량을 넘는지, 기존 대기 시간을 보지 않음).
- 결함 2: 준비 장벽(final route가 모든 prepare에 의존, readySubset은 같은 노드 제외). 3m-12 [P,R) 구간 F 6,336.
- 결함 3: 부하 상승을 버틸 여유 용량이 없음(대응 21~150초). 시작 60초 고정 실패 약 1,430건.
- 7m Smol D1 FAIL: Pod 삭제 후 재배치 동안 들어온 5건 지연(성공 36 < 기준 39).
- 결정: 결함 2(readiness 기반 점진 투입, maxUnavailable 0) → 결함 1(장치별 이용률 상한, 대기 시간 반영) 순서로 진행. S4는 후순위이며 게이트로만 판정.
- 12:40Z control-274: server 61987770(S3+S3b+State 수정) 배포, restart 0.

## 2026-10-31 — 디스크 정리(사용자 지시 "안 쓰는 옛날 것 과감히 삭제")

- 원인: 라운드마다 디스크가 줄어든 주범은 BuildKit 캐시였음(chill-builder 96GB 등). fast-deploy가 빌드할 때마다 쌓이고 정리되지 않음. 증거 디렉터리는 원인이 아니었음.
- 삭제: (1) 문서·hack·rounds·점수판·리플레이 입력 어디에서도 참조하지 않는 옛 증거 77개 디렉터리, 68GB(목록 `O/deployB/deleted-evidence-20261031.txt`). (2) chill-builder 캐시 79GB(20GB만 유지). 다른 builder는 7일 넘은 캐시 약 44GB 삭제. (3) 등록이 끊긴 firstfloor-experiment buildkit 컨테이너와 볼륨 20GB.
- 결과: 가용 292GB → 491GB.
- 유지: 참조되는 증거, O/harness/e5-*, -7·8d·223 행과 tag 10~12 행, registry 볼륨(97GB, runtime 7 digest 보호), 다른 프로젝트의 태그된 docker 이미지(약 200GB).

## 2026-10-31 — 가려운 곳 3건 조사 결과

- κ 보정(`local://kappa-calib.md`): CPU의 계획κ/포화 실행률 0.87/0.99/0.99로 κ는 대체로 맞음. CUDA는 과부하·배치 붕괴 상태에서 1.21/1.32배 낙관이며, 상태에 따라 달라서 고정 보정계수로 쓰면 안 됨.
- 진짜 결함: `capacityWeights`가 에너지 순으로 각 장치를 κ(p95≤1s 무릎)까지 채움 → 싼 장치가 ρ≈1에 고정되고 빠른 orangepi는 실측 용량의 57%만 받음. 부하가 오르면 재계획 전에 느린 장치가 무너짐. 무실패 실측 ρ 상한은 약 0.88~0.94.
- 결함 2(점진 투입) 패널(`agent://PanelPa`): 브리프의 κ 비례 투입은 orangepi를 무릎까지 밀어 위험함. 같은 노드·같은 UID 치환으로 범위를 좁혀야 함. 근본은 위의 weight 채움 규칙.
- 탐색 예산(`local://search-budget.md`): 실험 decision의 62%가 wall-clock BudgetCut이고, 인위적으로 멈추면 결정이 바뀌는 것을 확인(비결정성). 다만 11/12 분기는 30초 무절단 탐색에서도 유지되므로 그 분기의 원인은 아님. 후순위로 평가 수 기준 예산 검토.
- 디스크: BuildKit 자체 GC로 chill-builder 캐시를 20GB로 제한(`bd7dc822`). 가용 509GB.
- 다음: capacityWeights를 항상 κ 비례(동일 ρ)로 바꾸고 에너지 순 채움은 삭제하는 방향으로 패널 검토 중.

## 2026-10-31 — control-275 배포, weight 비례화 착수

- 13:11Z control-275: server와 dashboard를 `8a696dc3`(7788ce14 포함, 조언 세션 요청)으로 배포. 두 Pod 모두 restart 0, dashboard 응답 200. 게이트: 리플레이 PASS(기준 61987770), clean lint/test/arch/helm PASS. dashboard 테스트는 작업 트리에서 175/0. clean 사본에서만 2건 실패했는데 환경 문제로 추정. 새 builder 캐시는 3.8GB(GC 설정 동작 중).
- weight 비례화 패널 2/2 합의: capacityWeights의 에너지 순 채움 삭제, y는 최종 정수 weight로 계산, capacityEnergyLoad의 greedy 분기 삭제, carries/currentWeights 삭제. κ 비례는 SLO 정규화 부하의 minimax임(max λᵢ/κᵢ ≥ λ/Σκ). M/M/1 지연 최적은 아니지만 κ가 이미 장치별 SLO 무릎이라 타당함. 구현 진행 중(ImplWeights). fail-before 확인: 3m-12 결과가 기존 [138 359 216 287], 기대 [114 296 178 412].

## 2026-10-31 — weight A/B 판정 규칙(사용자 제안 채택)

- A/B(E5-3m만, 후보 506cd82a κ 비례 vs 기준 8a696dc3 에너지 순 채움, 교대 각 3회, tag 13~18)는 그대로 진행함.
- 판정: κ 비례가 SLO를 개선하고 에너지 증가가 잡음 범위 밖이면 → "상한 채우기"로 전환(싼 장치부터 ρ*×κ까지 채우고 넘는 양만 다음 장치로; HPA target utilization 발상). ρ*는 config 값 하나이고, 근거는 재계획 완료 전 예상 부하 증가 배율의 역수(E5-3m 계단 1.27배면 ≤0.78). 같은 ρ*를 결함 3(계획기 여유 용량 ρ*×Σκ ≥ 수요)에도 써서 파라미터 하나로 묶음. 별도 headroom 메커니즘은 추가하지 않음.
- 에너지 차이가 잡음 범위면 → κ 비례 유지(계획기가 더 적은 장치로 합격할 수 있어 에너지 이득 가능), 결함 3만 진행.
- A/B 보고에 장치별 요청당 증분 에너지와 유휴 전력 비중을 함께 표시함.

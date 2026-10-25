# 08. 최종 검증 계획 (P10)

[07](07-validation.md)의 측정 여섯 항목, 스케줄러 수용 시나리오 `SCH-AC-24`, `SCH-AC-25`, `SCH-AC-26`의 kind E2E,
E5 혼합 부하의 SLO와 에너지 판정을 한 번의 최종 검증으로 묶는다. 각 항목은 절차, 남길 증거, 통과 기준과 그 출처를 적는다.

명세는 수치 문턱을 두지 않는다. 실험의 성패와 해석은 분석자가 맡고(`docs/spec/README.md:62-64`), 실제 SLO와 에너지와
판단 오차는 종단 Serving 평가에서 검증한다(`docs/spec/scheduler/SRS.md:132-134`). 그래서 이 문서의 수치 기준은 모두 설계
문서에서 온 값이고 줄 번호로 출처를 단다. 설계에도 값이 없거나 설계 값이 지금 구조와 맞지 않는 곳은 7장의 사용자 결정이다.
줄 번호는 `feat/workload-pools` 5acaf73 기준이다.

## 1. 순서와 선행 조건

최종 통합의 순서는 **E5 → M5 정리 → main 병합 → main 소스 빌드 → 최종 배포 → P10**이다.
E5는 병합 전 측정 근거이고 P10 완료를 뜻하지 않는다. M5 변경을 마친 뒤 main에 병합하고,
병합된 main commit에서 image를 빌드해 최종 배포한다. 마지막 P10은 그 배포의 source commit,
image와 release revision을 고정해 아래 증거를 판정한다. 병합 전 branch image의 결과만으로
main 최종 배포 검증을 대신하지 않는다.

| 단계 | 내용 | 선행 | 산출 |
|---|---|---|---|
| S1 캠페인 종료 | 레인의 측정(BEO, DispatchOccupancyMeasurement, operation 비용)이 모두 terminal, Jetson이 가장 낮은 모드, 측정 전용 Workload 삭제 | 레인 | 프로파일, d, 비용 근거 |
| S2 배포 2 직전 값 | 지금 배포(rev 66)에서 S4와 같은 부하로 요청 경로 CPU, κ 표 계산 시간, Tick 시간을 잰다 | S1 | 비교의 앞 값 |
| S3 배포 2 | 묶음 5acaf73. 배포 전에 현재 image로 staticRouting을 적용해 모든 serving Pod의 routing annotation을 확인한다(`docs/design/platform/cleanup-m5.md:118`). ConfigMap `planSearchWorkers`, live 값(maxQueueSize 256/64/64, `--queue-limit` 1024, maxReplicas 8, maxInstances 16)이 [워크로드 관리 7장](../serving/workload-management.md#e5의-chillserver-메모리와-cpu-상한)의 계산 조건과 같은지 확인 | S2 | 배포 rev |
| S4 묶음별 지표와 G | 요청 경로 CPU, κ 표 계산 시간, Tick 시간, overload guard 발동 수(정상 부하에서 0), 게이트웨이 한계 G, edgemesh CPU와 메모리 | S3 | G, 앞뒤 비교 |
| S5 kind E2E | 3장. 코드만 쓰므로 S1과 무관하다. 시작 조건은 Running DispatchOccupancyMeasurement 0 | 없음 | 테스트 로그 |
| S6 E5 준비 | PLC와 SmolVLM2 SLO 승인(D2), `R_w` 계산, 단계 요청률 합 ≤ G 확인(`docs/design/serving/workload-pools.md:457-459`) | S4 | E5 입력 |
| S7 E5 | 4장. 2장의 항목 2, 3, 4가 쓰는 자료를 함께 모은다 | S6 | 관측 세그먼트, 판단 기록, 부하 기록, 열 기록 |
| S8 M3 실클러스터 | [측정 admission 6장](../serving/measurement-admission.md#6-실클러스터-확인) E-A1~E-A8. 측정이 노드를 측정 POOL로 옮기므로 E5가 끝난 뒤 한다 | S7 | 항목 5, `SCH-AC-26`의 실클러스터 증거 |
| S9 기준선 | 항목 6(D9로 정한 형태) | S7 | 기준선 실행 기록 |
| S10 오프라인 | 항목 1, 2의 에너지 비교, 3의 분석 | S7 | 표 |
| S11 M5 정리 | E5 뒤 남은 M5 문서·설정·구조 정리를 마치고 6장 형식의 사전 검증 기록을 정리 | S8~S10 | 병합할 M5 commit |
| S12 main 병합 | E5 근거와 M5 변경을 main에 병합 | S11 | main commit |
| S13 main 빌드 | 병합된 main commit의 소스로 최종 image를 빌드 | S12 | source commit과 image 참조 |
| S14 최종 배포 | S13 image로 최종 배포하며 외부 EdgeMesh의 고정 image·2 CPU/1Gi·OnDelete 선언은 보존 | S13 | 최종 release revision과 image |
| S15 P10 | 최종 배포에서 항목 4의 기록 크기와 RSS를 다시 확인하고, 나머지 항목의 근거·통과 여부 및 적용 source를 대조해 6장 최종 기록을 완성 | S14 | P10 최종 검증 기록 |

genesis1에서 하는 무거운 작업(빌드, `make test`, kind E2E, `-count`가 큰 테스트)은 `flock /tmp/chill-heavy.lock nice -n 19`로
돌리고, 시작 전에 측정이 진행 중이지 않은지 읽기 전용으로 확인한다.

```sh
kubectl get dispatchoccupancymeasurements -A   # testbed, Running이 있으면 기다린다
```

## 2. 07 검증 여섯 항목

### 항목 1. 계획기 오프라인 비교

- 지금 상태: E1 기록 판단 498건에서 target이 있는 457건이 모두 일치했고 에너지 차는 0 J다
  (`docs/design/serving/analytic-planner.md:213`). 같은 줄이 서버 계획기와 `v1alpha5` 세그먼트로 다시 하도록 남겨 둔다.
- 절차: E5 구간의 세그먼트와 E5 끝의 scheduler dump로 기록된 판단을 지금 계획기에 다시 넣는다.

```sh
hack/observations/fetch.sh <E5 시작 YYYYMMDDTHH> <E5 끝 YYYYMMDDTHH> e5
zcat e5/obs/*/scheduler.jsonl.gz > e5/records.jsonl
curl -s "$CHILL_API/api/v1/scheduler" > e5/scheduler.json
curl -s "$CHILL_API/api/v1/scheduler/profiles" > e5/profiles.json
GOMAXPROCS=4 go run ./hack/kappa -mode table -scheduler e5/scheduler.json -rows e5/rows.json
GOMAXPROCS=4 go run ./hack/kappa -mode compare -scheduler e5/scheduler.json -profiles e5/profiles.json \
  -records e5/records.jsonl -rows e5/rows.json
```

- 증거: compare 출력(판단 수, 기록된 target 수, 같은 target 수, 상태 분포, 다른 판단 표), 판단마다 `budgetCut`.
- 통과 기준: target 일치 또는 에너지 차 ≤ 5%(`07-validation.md:7`의 예시값, D3). 기록 판단 중 `budgetCut`이 참인 판단은 탐색
  시간에 따라 해가 달라질 수 있으므로 따로 센다(D5).

### 항목 2. E5 실측: 예측과 관측

- 절차: 4장 E5의 모든 단계. 1분 구간마다 Workload별로 판단의 SLO 예측과 관측을 짝짓는다(`docs/design/serving/workload-pools.md:476`).
  예측은 그 구간 판단의 `decision.capacity`에서 Workload의 `κ_new ≥ λ`이고 `Overload`가 없는 상태다(D4). 관측은 그 구간
  요청의 SLO 백분위 지연이 목표 L 이하인지다. 에너지는 판단마다 `ExpectedJoules / T`를 다음 판단까지 적분한 합과 비교 집합
  실측을 E5-6 1시간에서 비교한다(`docs/design/serving/analytic-planner.md:218`). 적분은 `scheduler.jsonl`의 판단 행으로
  `hack/observations/e1.sql`에 표 하나를 더해 계산한다.
- 증거: 구간 표(구간, Workload, λ, `κ_new`, 예측, 관측 백분위, L), E5-6 예측 합과 실측 합.
- 통과 기준: 예측이 충족인 구간의 95% 이상에서 관측도 충족, 재생 선택 대비 에너지 손해 ≤ 5%(`07-validation.md:8`의 예시값, D3).
  예측 에너지와 비교 집합 실측의 차 ≤ 2%(`docs/design/serving/analytic-planner.md:218`, D11).
- 참고값: E1 재실행의 비교 집합 예측 499,333 J, 실측 495,741 J(`docs/design/serving/workload-pools.md:504-505`).

### 항목 3. edge CPU 노드의 열 상태별 꼬리 지연

- 대상: rasp5(RPi5), orangepi(RK3588), lattepanda. 세 노드 모두 metric source에 `thermalMetrics`가 있다
  (`charts/chill/values-profiler.yaml:119-150,215-230`).
- 절차: E5 동안 genesis1에서 5초마다 열 지표를 시각과 함께 남긴다. 요청 `final` 행의 `node`, `batchSize`, `serviceMS`로 노드와
  배치 크기마다 SLO 백분위 실행시간 `l_q(b)`를 열 상태별로 계산하고, 그 노드 profile의 `l_q(b)`와 비교한다.

```sh
for h in raspberrypi-5 orangepi-5-plus lattepanda-3-delta; do
  (while sleep 5; do
     curl -s "http://155.230.35.213:18766/prometheus?hostname=$h" | grep '^device_thermal_' | sed "s/^/$(date -u +%FT%TZ) /"
   done > "e5/thermal-$h.log") &
done
```

- 증거: 노드, 배치 크기, 열 상태마다 표본 수와 `l_q(b)`, profile 값과의 차, 열 상태 구간 길이.
- 통과 기준: 열 상태 사이 `l_q` 변동이 SLO 여유 안(`07-validation.md:9`). 열 상태의 구분과 SLO 여유의 정의는 D6이다.

### 항목 4. 판단 기록과 ChillServer 자원

- 지금 상태: E1 재실행에서 판단 기록 중앙값 185,359 B로 목표 100 KB를 넘었고(D-15), RSS 최대 111.4 MiB, 재시작 0, 관측 PVC
  273 MiB/h, Tick p99 6,166 ms(2 req/s)였다(`docs/design/serving/decision-record-diet.md:85-91`). 그 뒤 M1과 M4가 기록 형태를
  바꿨으므로 E5에서 다시 잰다.
- 절차: E5 단계마다 판단 줄 크기의 중앙값과 최대, ChillServer `container_memory_working_set_bytes` 최대와 재시작 수, 시간 단위
  세그먼트 크기와 staging(`/tmp`) 최고 사용량, `evaluationElapsedNanos` p50과 p99를 기록한다
  (`docs/design/serving/workload-pools.md:471`).

```sh
zcat e5/obs/*/scheduler.jsonl.gz | awk '{print length($0)}' | sort -n | awk '{a[NR]=$1} END {print "median", a[int(NR/2)+1], "max", a[NR]}'
duckdb < hack/observations/e1.sql   # Tick 표
kubectl -n chill-system get pod -l app.kubernetes.io/component=serving-entrypoint -o jsonpath='{.items[*].status.containerStatuses[*].restartCount}'
```

- 통과 기준: 기록 ≤ 100 KB, RSS가 limit 아래이고 OOM 재시작 0, PVC 증가는 기록, Tick p99는 기록
  (`docs/design/serving/decision-record-diet.md:76-79`). E5의 Tick 목표는 D7이다.

### 항목 5. 단일 writer 경쟁

- 지금 상태: 07의 envtest 1000회 대신 M3 설계는 fake client 위 동시 test 하나로 정했다
  (`docs/design/serving/measurement-admission.md:195-202`). 그 test가 `TestConcurrentRequestsNeverOverlapMeasurementAndServing`이다.
- 절차: 같은 test를 1000회 `-race`로 돌리고, 측정 객체 삭제 뒤 자동 반환과 ChillServer 재시작은 S8의 E-A7로 확인한다. 측정 구간
  전력 변경(Node label 경로 포함)의 무효 판정 수는 E-A8로 센다.

```sh
flock /tmp/chill-heavy.lock nice -n 19 env GOFLAGS=-buildvcs=false \
  go test -race -count=1000 -run TestConcurrentRequestsNeverOverlapMeasurementAndServing ./internal/serving/kube/
```

- 증거: test 출력, E-A1~E-A8 표(측정 객체 상태, taint 시각, 반환 시각, Pod 목록, `measurement-invalid` 수).
- 통과 기준: 겹침 0, 자동 반환(`07-validation.md:11`), E-A1~E-A8의 기준(`docs/design/serving/measurement-admission.md:210-217`).
  test 대체와 node agent kill의 포함 여부는 D8이다.

### 항목 6. 기준선 비교

- 지금 상태: B3는 비교 기준선으로 두기로 했고([04](04-b3-decentralized-baseline.md)) 구현은 없다. 07은 통과 기준을 두지 않는다
  (`07-validation.md:12`). 명세는 제어요소를 하나씩 끈 비교와 정적 배포 비교를 요구하고, 정적 구성은 선택 근거를 남긴다
  (`docs/spec/README.md:213-216`). 정적 구성은 자동제어 밖 Workload의 수동 prepare로 만든다
  (`docs/design/serving/scheduler-operations.md:127`).
- 절차(D9에서 정한 기준선으로): E5-2 버스트와 E5-5 급감과 재급증을 같은 seed와 요청률로 기준선에서 다시 돌린다. 단계마다 전환
  수(진동), 전체 POOL 에너지, Workload별 SLO 결과를 E5와 같은 방법으로 센다.
- 증거: 기준선 구성과 선택 근거, 단계별 비교 표.
- 통과 기준: 없음. B1 기여의 근거로 기록한다.

## 3. SCH-AC-24, SCH-AC-25, SCH-AC-26 kind E2E

`internal/serving/kube/kind_e2e_test.go`(build tag `e2e`)가 kind API 위에서 스케줄러 controller를 돌린다. Node, Pod, Service,
측정 taint, Pod를 소유하는 Workload는 실제 API 객체이고 측정 루트, profile, meter, 전력, native 상태는 가짜다. 테스트는 현재
context가 `kind-*`가 아니면 멈춘다.

| 시나리오 | test | 확인하는 결과 | 실클러스터 짝 |
|---|---|---|---|
| `SCH-AC-24` (`docs/spec/scheduler/SRS.md:161`) | `TestE2EKindSCHAC24FirstConfiguration` | 서빙한 적 없는 노드에만 측정 근거가 있는 Workload가 그 노드에서 시작, 다른 가용 노드는 가장 낮은 모드, 설정 여유로 평가, 다음 제어 주기에 다시 판단 | E5-0(`docs/design/serving/workload-pools.md:463`) |
| `SCH-AC-25` (`SRS.md:162`) | `TestE2EKindSCHAC25Coexistence` | 꺼진 Workload의 인스턴스와 노드 모드 유지, 근거 없는 켜진 Workload의 측정 근거 대기 표시, 근거 있는 Workload의 판단 계속 | E2(`workload-pools.md:448`), D10 |
| `SCH-AC-26` (`SRS.md:163`) | `TestE2EKindSCHAC26MeasurementAdmission` | 가용 노드가 측정 POOL로 갔다가 반환, 서빙 노드 거절과 사유, 외부 Pod 노드 측정과 그 Pod 기록 | E-A1, E-A2, E-A3(`measurement-admission.md:210-212`) |

```sh
cd <worktree>
./hack/kind-up.sh                                   # 있는 kind-chill이면 kubeconfig만 내보낸다
export KUBECONFIG="$PWD/.kube/chill-kind.config"
kubectl config current-context                      # kind-chill
kubectl --kubeconfig <testbed kubeconfig> get dispatchoccupancymeasurements -A   # Running 0일 때 진행
flock /tmp/chill-heavy.lock nice -n 19 env GOFLAGS=-buildvcs=false \
  go test -tags e2e ./internal/serving/kube/ -run 'TestE2EKindSCHAC2[456]' -v -count=1 2>&1 | tee e2e-sch-ac.log
```

- 증거: `e2e-sch-ac.log`, 커밋 SHA, kind Node image(`hack/kind-up.sh:7`).
- 통과 기준: 세 test PASS. 시나리오의 확인 결과는 `SRS.md:161-163`의 문장 그대로다.

## 4. E5 혼합 부하의 SLO와 에너지 판정

### 4.1 명세 근거

| 근거 | 내용 |
|---|---|
| `docs/spec/README.md:33-36` | SLO는 Workload가 정한 백분위와 목표 시간이다 |
| `docs/spec/README.md:45-48` | SLO 지연은 CHILL 수신부터 응답 반환 완료까지이고 배정이나 배칭으로 시작 시각을 다시 잡지 않는다 |
| `docs/spec/README.md:51-53` | SLO를 만족하는 선택 중 에너지가 가장 유리한 선택을 찾는다. 처리량은 부하 감당 판단 지표다 |
| `docs/spec/README.md:55-58` | 버스트, 전환 시간, 전체 POOL 한계로 SLO를 지킬 수 없는 상황은 실행 오류와 구분해 관측하고, 만족 조건과 위반 조건을 실험으로 확인한다 |
| `docs/spec/README.md:62-64` | 완료, 실패, 거절, 취소, 관측 시점 미완료를 구분해 기록하고 성패 해석은 분석자가 한다 |
| `docs/spec/README.md:198-206` | 평가 시나리오 표(일정, 증가와 감소, 버스트, 급감과 재급증, 이기종 혼합, 한계 초과, 실패) |
| `docs/spec/README.md:208-211` | Workload별 지연 분포와 처리 결과, 같은 시나리오의 총에너지를 SLO 목표와 함께 해석. 에너지는 전환 구간을 포함한 전체 POOL, 활성 POOL은 따로, 처리 결과 수로 작업량과 연결 |
| `docs/spec/README.md:218-221` | 실행마다 시나리오, profile과 설정, 초기 구성, 제어 동작과 상태 변화, 요청 결과, 전력을 남기고 예측과 실측 차를 본다 |
| `docs/spec/scheduler/SRS.md:152-153,158,160-161` | `SCH-AC-15`, `SCH-AC-16`, `SCH-AC-21`, `SCH-AC-23`, `SCH-AC-24`의 확인 결과 |

### 4.2 단계별 판정

E5 단계, 시간, 요청률은 `docs/design/serving/workload-pools.md:461-469`다. Workload SLO는 tight p95 1 s
(`workload-pools.md:456`), PLC와 SmolVLM2는 `hack/kappa -mode slo` 제안을 사용자가 승인한 값이다(`workload-pools.md:457`, D2).

| 단계 | 명세 행 | 판정 | 수치 기준과 출처 |
|---|---|---|---|
| E5-0 시작 | `SCH-AC-24` | 세 Workload 모두 측정 근거가 있는 노드에 첫 인스턴스, 나머지 가용 노드 가장 낮은 모드 | 상태 판정(`SRS.md:161`) |
| E5-1 일정 | 일정 부하, 이기종 혼합, `SCH-AC-23` | Workload별 SLO 충족, 측정 조건이 맞는 profile 사용, Workload별 지연과 처리 결과, 전체 에너지 제공 | SLO 충족의 판정 단위는 D1, 예측과 관측은 항목 2 |
| E5-2 버스트 | 버스트, `SCH-AC-21` | 대응 시작 시각, 같은 노드의 영향받는 모든 Workload의 전환 중과 최종 SLO를 함께 평가한 판단 기록 | 버스트 구간 SLO 위반은 원인과 함께 관측(`README.md:55-58`). 판단 기록의 Workload별 전환 중 판정은 상태 판정 |
| E5-3 증가 | 증가, `SCH-AC-15` | 같은 제어 로직이 새 부하를 반영, 확장 순서, 전환 구간 SLO | 비율 단계 0.5, 0.7, 0.9 `R_w`는 용량 안이므로 SLO 판정은 D1 |
| E5-4 한계 | 한계 초과, `SCH-AC-16` | `Overload` 판단과 원인, 실제 지연과 처리 결과 구분, 요청 수명 유지 | 위반은 관측 대상(`README.md:55-58`). 판정은 `Overload` 기록과 처리 결과 분류의 존재 |
| E5-5 급감과 재급증 | 급감과 재급증, 감소, `SCH-AC-15` | 반복 전환의 비용(J, 시간)과 대응 | 기록. 기준선 비교는 항목 6 |
| E5-6 soak | 일정 부하 | 1시간 예측 에너지와 비교 집합 실측, 전체 POOL 실측, `completionResidualNanos`와 설정 여유 | 예측과 실측 차 ≤ 2%(`analytic-planner.md:218`, D11), SLO는 D1 |

### 4.3 에너지 판정

- 보고 값(`README.md:208-211`): 단계마다 전체 POOL 에너지(전환 구간 포함), 활성 POOL 에너지, Workload별 처리 결과 수, 완료 요청당
  J. 명세는 문턱을 두지 않는다.
- 비교 값: E5-6의 예측과 비교 집합 실측(≤ 2%, D11), 재생 선택 대비 손해(≤ 5%, D3), 기준선과의 차(항목 6, 기록).

```sh
go run ./hack/observations/power --dir e5/obs --metric-sources <run의 metric-sources YAML> \
  --nodes <kubectl get nodes -o json> --from <단계 시작 RFC3339> --to <단계 끝 RFC3339>
duckdb < hack/observations/e1.sql   # e5에서, loadgen/<stage>.jsonl과 요청 표
```

### 4.4 함께 남길 값

모든 단계에서 Tick p50과 p99, ChillServer CPU와 RSS, 관측 저장 증가, 탐색 기록(`neighborhood`, `evaluated`, `durationNanos`,
`budgetCut`), Tick CPU, 게이트웨이 지연 p99(탐색 구간과 그 밖), 1분 구간 SLO 비율을 남긴다
(`docs/design/serving/workload-pools.md:471-478`). 단계 경계, seed, 단계별 `R_w`와 G, 사용한 profile과 설정이 실행 기록에 들어간다.

## 5. 증거 보관

실행마다 관측 window, 판단 기록, 측정 객체 상태를 보존하고 ChillServer 메모리와 관측 저장 사용량을 함께 기록한다
(`docs/design/serving/workload-pools.md:443`). 실행 디렉터리는 `fetch.sh`의 `obs/<hour>/<stream>.jsonl.gz`, `loadgen/<stage>.jsonl`,
scheduler와 profiles dump, 열 기록, test 로그를 담는다.

## 6. 결과 기록 형식

| 항목 | 실행 시각과 rev | 증거 위치 | 측정값 | 기준 | 판정 |
|---|---|---|---|---|---|

07 여섯 항목, kind E2E 세 시나리오, E5 일곱 단계가 각각 한 행이다. 판정은 통과, 미달, 기록 셋 중 하나이고 미달이면 07의
"기각 시" 열을 따른다.

## 7. 사용자 결정

| # | 결정할 것 | 근거 |
|---|---|---|
| D1 | E5 SLO 충족의 판정 단위(단계 전체 백분위 ≤ L, 또는 1분 구간 충족 비율과 그 문턱)와 판정 대상 단계(E5-1, E5-3, E5-5, E5-6). 전환 구간 포함 여부 | 명세는 성패 해석을 분석자에게 둔다(`README.md:62-64`) |
| D2 | PLC와 SmolVLM2의 SLO(백분위와 L) | `workload-pools.md:457`, 제안 뒤 승인 |
| D3 | 07의 예시 기준(target 일치 또는 에너지 차 ≤ 5%, 95% 구간, 에너지 손해 ≤ 5%)을 확정할지. 재생 선택이 Tick에서 빠졌으므로 "손해"의 기준 해를 무엇으로 둘지(같은 노드 계획 공간의 전수 최소 오프라인 계산, 항목 6의 기준선, 또는 예측과 실측 비교만) | `07-validation.md:5,7-8`, `analytic-planner.md:237`, [용량표 근거 7장](../serving/capacity-table-evidence.md#7-계획-탐색) |
| D4 | 구간별 예측을 용량 판정(`κ_new ≥ λ`, `Overload` 없음)으로 둘지. 계획기는 예측 백분위를 판단마다 내지 않는다 | `analytic-planner.md:17-19,77` |
| D5 | 항목 1에서 `budgetCut` 판단의 불일치를 실패로 셀지 따로 기록할지. 07의 "SLO 판정이 재생보다 낙관적인 경우 없음"을 항목 2의 실측 비교로 대신할지 | `07-validation.md:7`, `analytic-planner.md:238` |
| D6 | 항목 3의 열 상태 구분(throttle active, latched, 온도 구간)과 SLO 여유의 정의(L − (profile `l_q(b)` + b × d + m) 등), 표본 간격 5초 | `07-validation.md:9` |
| D7 | E5의 Tick 목표. `analytic-planner.md:217`의 p99 ≤ 100 ms, CPU 500m은 E1 규모의 값이고 E5 탐색 시간 예산은 제어 주기의 절반이다 | `analytic-planner.md:217,238` |
| D8 | 항목 5에서 fake client `-race -count=1000`이 envtest 1000회를 대신하는지, node agent Pod kill을 실클러스터에서 할지(클러스터 변경) | `07-validation.md:11`, `measurement-admission.md:195` |
| D9 | 항목 6의 기준선: B3 구현, 명세의 정적 구성 비교와 제어요소 끄기 비교, 또는 생략. 다시 돌릴 단계 | `README.md:213-216`, [04](04-b3-decentralized-baseline.md) |
| D10 | `SCH-AC-25`를 kind E2E로 닫을지, 실클러스터 E2를 따로 돌릴지 | `workload-pools.md:448` |
| D11 | 예측 에너지와 비교 집합 실측 차 ≤ 2%를 세 Workload E5-6에도 쓸지 | `analytic-planner.md:218` |

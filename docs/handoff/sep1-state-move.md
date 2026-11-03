# #1 state 이동 재개 인계

## 상태와 재개 조건

사용자 결정 B에 따라 #1은 여기서 중단한다. **E5 회차가 안정된 뒤 사용자/오케스트레이터가 재개를 승인할 때만** 이 브랜치의 cutover를 재개한다. 측정 표를 채우는 실행 경로에 이 구조 변경을 끼워 넣지 않는다.

- 녹색 체크포인트: `refactor/sep1-kube`, `5cdf4cbe` 위의 getter 정리 `b21ee919`.
- 미완료 실제 이동: `refactor/sep1-state-wip`, 코드 보존 커밋 `21219663`.
- **WIP는 merge/deploy 대상이 아니다.** Production build는 통과하지만 kube 테스트는 private fixture 접근 때문에 컴파일되지 않는다. 이 문서는 WIP 브랜치 전용이며 main에 넣지 않는다.
- 실제 작업 트리: `/home/genesis1/.cache/chill-wt/sep1`. `/home/genesis1/ETRI/chill`은 수정하지 않는다.

## 이미 끝난 것

1. 녹색 브랜치에서 management lifecycle과 physical operations 분리가 끝났다. 단일 reservation authority와 기존 lock을 유지한 concrete Start/Complete/Cancel/Retire, snapshot/decision publication, lease, handoff/effect/history/replacement API 정리도 끝났다.
2. WIP에서 실제 장부와 native/cluster Observer 구현을 `internal/serving/state`로 옮겼다. `state.ClusterState`가 한 장부와 제거 표시를 소유하며 controller는 `cluster.Reservations()`로 동일 객체를 받는다. Raw mutex/map/Run 필드는 공개하지 않았다.
3. Controller configuration도 원래의 같은 mutex를 계속 공유한다. `LockTick → LockState` 순서와 각 acquisition/release 위치, callback 실행 위치를 보존했다. Configuration은 원래 잠금 내부 위치에서 동기 callback으로 읽는다.
4. Production import cycle을 제거했다. 유일하게 드러난 DTO cycle인 `state → operations.MeterDeclaration → state`는 `MeterDeclaration` 실제 선언을 state 안쪽으로 옮겨 해소했다. Field 순서, JSON tag, evidence key/value는 바꾸지 않았다.
5. 실제 이동에 맞춰 production callers와 테스트의 namespace references를 옮겼다. Ledger 단위 테스트 두 개는 `state/cluster_state_transitions_test.go`로 실제 이동했다.
6. Ponytail의 `snapshotWorkloadsLocked` 지적은 녹색 브랜치 `b21ee919`에서 제거했다. WIP도 `SnapshotLocked().Workloads`를 사용하고 해당 getter는 없다.

## 확인한 것과 아직 확인하지 못한 것

- 녹색 `5cdf4cbe`: build/변경 패키지 테스트 PASS, lock 관련 14개 `-race -count=200` PASS(525.361s).
- 녹색 getter 정리 `b21ee919`: kube 패키지 테스트 PASS(43.605s), `make lint` PASS.
- WIP `21219663`: `go build ./cmd/... ./internal/serving/...` PASS. State 단위 테스트, application/server 테스트 PASS.
- WIP kube 테스트: **컴파일 실패**. 예: `admission_test.go`의 `manager.observer.kube/native`, `controller.mu/runs` 접근. 이를 성공으로 보고하지 않는다.
- WIP의 전체 lint/arch-check/make test/gopls-check와 최종 kube race는 완료되지 않았다. 새 state dependency 정책과 이동 문서도 최종 cutover와 함께 정리해야 한다.
- 이전 전체 kube race에서 DATA RACE 없이 AC39 timeout이 남았던 이력은 별개다. 이름은 `TestTickIndependentExpansionExecutesNativeBeforePowerCompletesAC39`의 slow-power/unconfirmed-source/pending-kappa/preparing-source이다. 회피하거나 새 실패를 이 이력으로 덮지 않는다.

## 남은 범위: 테스트 소유권과 실제 package cutover

근본 원인은 여러 owner의 private 상태를 한 테스트 fixture가 함께 조립/수정하는 것이다. 예를 들어 기존 `scheduler_continuation_test.go`는 Controller config/runtime와 actionRun 내부를 함께 만지고, `reservation_fixture_test.go`는 controller에 장부를 직접 설치한다. 이 파일을 그대로 state 내부 테스트로 옮겨 reconcile을 import하면 `state(test) → reconcile → state` cycle이 된다. Reconcile에 남겨 raw state internals를 쓰는 것도 안 된다.

- 장부 단위 테스트는 state 내부로 분리한다.
- Controller 통합 테스트는 실제 state Start/Complete API와 기존 공개 `State()`/reservation projections를 쓰는 black-box fixture로 재구성한다.
- Native client/Kubernetes client는 실제 constructor input으로 조립하고 fixture가 그 dependency handle을 보유한다. Observer private 필드를 나중에 교체하는 방식을 새 production accessor로 정당화하지 않는다.
- 기존 시나리오, 테스트 이름, assertion 수를 줄이지 않는다. Pointer/배선 중심 검사는 동일한 소비자 관찰 결과에 대한 검사로 바꾸되 조건을 약화하지 않는다.
- 단위 테스트 이동 후 테스트 때문에만 승격된 exports를 다시 최소화한다. 최종 controller는 `internal/serving/reconcile`로 이동한다. `scheduling`은 pure planner용 이름이므로 쓰지 않는다.
- Refactor-reviewer용 read-only 계산 경계는 **아직 구현하지 않았다**. #1 재개 이후 별도 커밋이다. 정상 경로의 retain 쓰기 → catalogRead publication → costs 읽기 → preparations 쓰기 → acceptance → 관측 순서를 유지하면서 재사용 증거/준비물의 공유 계산과 쓰기를 분리한다. 검증 경로는 같은 계산을 쓰되 쓰기와 runtime/observer/관측 loop를 만들지 않는다. `objectStore(opts, httpClient)` transport 주입도 그 별도 범위다.

### 영향 테스트 파일 목록: 40개

앞선 보고의 “kube 테스트 40개 파일”은 엄밀히는 **AST 이동에서 namespace 변경이 발생한 영향 테스트 전체 40개**다. 아래는 kube 31개와 다른 consumer 9개를 구분한 정확한 목록이다. 모든 40개가 미수정이라는 뜻은 아니며, 추가 private 접근 여부는 재개 시 감사해야 한다. 이 구분 없이 “40개 kube 파일 전부”라고 보고하지 않는다.

#### Kube 출신 31개

- `internal/serving/kube/admission_test.go`
- `internal/serving/kube/catalog_reuse_test.go`
- `internal/serving/kube/cluster_state_transitions_test.go` — WIP에서는 `internal/serving/state/cluster_state_transitions_test.go`로 이동 완료
- `internal/serving/kube/late_probe_test.go`
- `internal/serving/kube/manager_test.go`
- `internal/serving/kube/measurement_sequence_test.go`
- `internal/serving/kube/native_service_test.go`
- `internal/serving/kube/observer_budget_test.go`
- `internal/serving/kube/observer_cluster_test.go`
- `internal/serving/kube/observer_refresh_ready_test.go`
- `internal/serving/kube/observer_test.go`
- `internal/serving/kube/occupancy_scientific_test.go`
- `internal/serving/kube/operations_test.go`
- `internal/serving/kube/pending_prepare_test.go`
- `internal/serving/kube/reservation_compensation_test.go`
- `internal/serving/kube/reservation_empty_dag_test.go`
- `internal/serving/kube/reservation_fixture_test.go`
- `internal/serving/kube/reservation_late_binding_test.go`
- `internal/serving/kube/reservation_test.go`
- `internal/serving/kube/reservation_tick_test.go`
- `internal/serving/kube/reservation_untouched_test.go`
- `internal/serving/kube/reset_test.go`
- `internal/serving/kube/scheduler_bridge_test.go`
- `internal/serving/kube/scheduler_cleanup_test.go`
- `internal/serving/kube/scheduler_continuation_test.go`
- `internal/serving/kube/scheduler_cpu_contract_test.go`
- `internal/serving/kube/scheduler_expected_test.go`
- `internal/serving/kube/scheduler_replacement_test.go`
- `internal/serving/kube/scheduler_test.go`
- `internal/serving/kube/scheduler_transition_test.go`
- `internal/serving/kube/static_routing_test.go`

#### 다른 consumer 9개

- `cmd/chill-server/http_routes_test.go`
- `cmd/chill-server/live_measurements_test.go`
- `cmd/chill-server/scheduler_fixture_test.go`
- `internal/serving/application/profiles_test.go`
- `internal/serving/application/scheduler_service_test.go`
- `internal/serving/httpapi/scheduler_projection_test.go`
- `internal/serving/httpapi/scheduler_summary_test.go`
- `internal/serving/httpapi/scheduler_test.go`
- `internal/serving/measurement/occupancy_http_test.go`

## AST 기준선과 비교 계약

- 기준선: `/home/genesis1/.cache/chill-wt/sep1-tests-before-state.json`.
- 생성기: `/home/genesis1/.cache/chill-wt/sep1-test-inventory.go`.
- 범위는 **internal/serving 전체**의 `*_test.go`에 있는 top-level `Test*` 함수다. 테스트 이름 783개, unique name 783개, assertion call 3,642개다. Runtime subtest 횟수나 coverage 비율을 뜻하지 않는다.
- Assertion count는 AST의 `Fatal/Fatalf/Error/Errorf/Fail/FailNow` selector call 수다. 실행 횟수가 아니다.
- 이동 후 같은 생성기로 after JSON을 만들고 이름 집합 차집합이 양방향 모두 비었는지 확인한다. 빠진 이름 0, 중복 이름 0을 보고한다. Assertion 합계와 함수별 감소도 확인해 감소를 허용하지 않는다. 이 전후 비교는 아직 수행하지 않았다.
- 위 cache 파일은 Git 밖의 인계 자료이므로 재개 전에 존재를 확인한다. 없으면 녹색 `5cdf4cbe`/`b21ee919`의 별도 cache worktree에서 같은 AST inventory를 다시 생성한다. WIP를 baseline으로 삼아 누락을 정상화하지 않는다.
- 재현 예:

```sh
env GOTMPDIR=/home/genesis1/.cache/chill-wt TMPDIR=/home/genesis1/.cache/chill-wt \
  go run /home/genesis1/.cache/chill-wt/sep1-test-inventory.go \
  /home/genesis1/.cache/chill-wt/sep1 \
  /home/genesis1/.cache/chill-wt/sep1-tests-after-state.json
```

기계적 이동 근거는 `sep1-state-move.json`, `sep1-state-runtime-params.json`, `sep1-meter-move.json`(같은 cache 디렉터리)에 있다. 기존 source position 계획은 최신 merge 후 재사용하지 말고 AST에서 새로 계산한다.

## 금지 사항과 품질 기준

- Raw mutex/map/Run field export, 테스트 전용 production API, proxy/shim/re-export/forwarder 금지.
- 두 번째 장부/lock, lock 분리, 상호 pointer, late binding, nil fallback, 새 거절/fatal 금지.
- Behaviour/JSON/CRD/evidence schema·key/value/ordering/authorization 변경 금지.
- 상태 owner 밖에서 private 상태/lock을 직접 만지지 않는다. 인터페이스는 실제 package boundary가 요구하는 최소 소비자 계약만 허용한다.
- `chill-dashboard/**`, `internal/queryapi/**`를 수정하지 않는다. Query API에는 serving import를 추가하지 않는다. Cmd query의 순수 import/qualifier 이동만 승인 범위다.
- `hack/**`는 architecture coverage의 component case/list mapping 외 수정하지 않는다. `/tmp`와 원본 `/home/genesis1/ETRI/chill`을 작업 공간으로 쓰지 않는다.
- 수정/커밋 전에 `intent/e5-iteration-loop-20261104.md`의 코드 수정 품질 기준과 `intent/orchestrator-mindset.md`를 다시 읽는다. 같은 테스트 순환을 세 번 넘게 우회하려 하지 말고 원래 측정 목표/상위 fixture 소유권을 다시 점검해 보고한다.

## 재개 절차와 예상 충돌

1. 사용자 재개 승인과 E5 안정 조건을 먼저 확인한다. 이 WIP를 main에 직접 merge하지 않는다.
2. 별도 cache worktree에서 WIP branch를 열고 최신 main을 merge한다. 원본 ETRI checkout은 수정하지 않는다.

```sh
git -C /home/genesis1/.cache/chill-wt/sep1 fetch -q origin
git -C /home/genesis1/.cache/chill-wt/sep1 switch refactor/sep1-state-wip
git -C /home/genesis1/.cache/chill-wt/sep1 merge origin/main
```

3. 예상 충돌 지점:
   - 삭제/이동된 `kube/observer*.go`, `cluster_state*.go`, `reservation.go`, `scheduler_state_*.go`, `target_scope.go`와 새 state 파일. Main의 관측/예약 결함 수정은 **새 owner 파일로 이식**하고 옛 경로를 shim으로 살리지 않는다.
   - `cmd/chill-server/{server.go,scheduler_wiring.go}`, application dependencies/scheduler service, HTTP handlers의 constructor/type imports.
   - `operations/measurement.go`와 `state/meter.go`의 MeterDeclaration 이동 및 각 consumer qualifier.
   - 녹색 getter 정리 `b21ee919`와 WIP의 `SnapshotLocked().Workloads` 대응.
   - Test namespace/fixture 수정과 main의 추가 회귀 테스트. 테스트를 어느 쪽 것도 버리지 않는다.
   - Main 347798dd에 포함된 `scheduler_plan.go`, `scheduler_validate.go`, `scheduler_capacity_nodes.go`의 인스턴스 단위 제외 결함 수정은 보존한다. 이번 WIP가 그 pure planner 수정의 대체물이 아니다.
4. Owner별 fixture 재구성으로 kube 테스트 컴파일을 회복한다. 이름/검사 기준선 비교와 changed-package build/test가 먼저다. API를 테스트 편의를 위해 넓히지 않는다.
5. 실제 state/Observer 이동이 독립적으로 녹색일 때 commit한다. 다음에 controller reconcile 이동, 그다음 read-only 공유 계산 경계를 별도 commit으로 진행한다. 각 새 package는 arch-lint, coverage mapping, design 문서를 함께 반영한다.
6. 반복 검증은 변경 패키지만 한다. Lock 관련 행동 테스트에만 `-race -count=200`을 쓴다. 최종 push 직전에 `make lint`, `make arch-check`, `gotest.sh make test`, `make gopls-check`, 전체 kube race를 한 번 수행하고 실제 결과만 보고한다. 정식/ponytail 리뷰도 책임·의존 방향·과구현·과검증을 명시적으로 확인한다.

## 위험

핵심 위험은 private fixture 조작을 공개 행동 시나리오로 바꾸면서 race/예약 유지/hand-back/late-probe proof를 약화하는 것이다. 40개는 영향 목록이지 단순 namespace 교체 작업량이 아니며, 소유권 분리 이후 추가 test-only exports와 import-cycle 검토가 필요하다. Production build PASS를 테스트 완료나 배포 가능으로 해석하지 않는다. 재구성 완료 시각은 확정하지 않았다.

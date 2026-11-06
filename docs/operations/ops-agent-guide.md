# 운영 에이전트 가이드

- **증거 먼저:** 기존 `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy` 기록과 라이브 값의 출처를 확인한다. 명령·실패 원형을 보존하며 관측과 추론을 구분한다.
- **공용 잠금:** 배포/빌드는 `/tmp/chill-heavy.lock`의 배타 잠금을 공유한다. 점유 중이면 중단한다. 다른 운영자의 작업과 겹쳐 실행하지 않는다.
- **시간 상한:** 배포 명령은 제한 시간 안에 종료한다. recipe의 `buildSeconds` 기본 1800초, `upgradeSeconds` 기본 900초(Helm `--wait --timeout`, 프로세스 제한은 선택값+30초), `readinessSeconds` 기본 480초이며 각 사용자 명령에 양의 `seconds`를 명시한다. 서버 초기화가 길어지면 운영자가 `upgradeSeconds`를 늘린다. timeout의 stdout/stderr도 보존하고 실패 시 증거 경로를 보고한 뒤 멈춘다.
- **금지:** 강제 삭제, finalizer 제거, 자동 재시도, 암묵적인 reset/rollback 금지. 실패 후에는 부분 상태와 증거를 검토하고 새 recipe/새 증거 디렉터리로 명시적인 roll-forward만 수행한다.
- **측정 progress 캡처:** CR 생성 직후 `python3 hack/measure/capture-progress.py --node NODE --measurement CR_NAME --output-dir /tmp/NEW --wait-for-pod 300 --capture-load`를 실행한다(또는 백그라운드 선실행 후 CR 생성). CR의 Workload 소유 Pod를 2초 간격으로 최대 300초 기다리고 첫 행부터 timestamped follow를 시작한 뒤 `READY`를 출력한다.

## Recipe 기반 roll-forward 배포

기존 `hack/fast-deploy.sh` 구현을 `hack/deploy/main.py`의 얇은 실행기로
**교체**했다. 이전 배포 모드나 플래그를 보존하는 shim은 없다.
recipe의 전체 작성 예제와 단일 계약은
[hack README](../../hack/README.md#recipe-driven-roll-forward-deployment)를 따른다.

```sh
hack/fast-deploy.sh run /absolute/private/recipe.json --dry-run
hack/fast-deploy.sh run /absolute/private/recipe.json --inspect --evidence /absolute/private/new-inspection
# 아래 실제 실행은 별도 승인 후 배포 담당 세션 main만 수행:
hack/fast-deploy.sh run /absolute/private/recipe.json --execute --evidence /absolute/private/new-deployment
# chart drift가 있다면 검토한 inspection의 chart-diff.txt 원본을 정확히 승인:
# --chart-diff-approved /absolute/private/reviewed-chart-diff.txt
```

- 기본값과 `--dry-run`은 로컬 계획만 출력한다. 클러스터/레지스트리 접근, 이미지 빌드, 잠금, 영구 증거 쓰기가 없다. 선택한 coverage projector는 로컬에서 Go 컴파일한다. `--dry-run`을 빼는 것은 실행 승인이 아니다.
- `--inspect`는 명시한 kubeconfig/context의 Helm values/manifest/hooks와 source chart를 읽어 렌더한다. custom phase 명령, 이미지 빌드/발행, 클러스터 쓰기를 하지 않는다. 실제 startup validator와 coverage projector는 로컬 컴파일할 수 있다. inspect와 execute 모두 존재하지 않는 새 `--evidence` 경로가 필수다.
- `--execute`만 배포/발행을 허용한다. source(또는 `--source SHA`), kubeconfig, context, namespace, release, registry, apiEnvironment, 양의 현재 `expectedRevision`·`minFreeBytes`, 비어 있지 않은 `images`를 명시한다. 소스는 full commit으로 해석하고 `git archive`로 고정한다. 과거 문서의 SHA/revision/승인은 현재 승인이 아니다.
- Python3/PyYAML/git/tar가 필요하고, 해당 실행 단계는 Helm, kubectl, curl, 빌드 시 Docker buildx를 사용한다. registry HTTP 사용은 `registryScheme: "http"`로 명시한다. 인증은 기존 도구 설정을 사용하고 credential을 recipe/buildArgs/argv에 넣지 않는다.
- execute는 `/tmp/chill-heavy.lock` 배타 잠금을 획득한다. inspect는 잠금을 잡지 않는다. `minFreeBytes`와 `maxDiskUsedPercent`(기본 85)를 확인한다. 실제 builder의 `buildx du`를 빌드 전후에 기록하며 이미지/cache/data를 자동 prune하지 않는다.
- 기본 live **user values** 위에 명시 source values를 합치고 Helm `--reset-values`를 사용한다. `--reuse-values`나 계산된 `--all` defaults의 동결이 아니다. `replaceValues: true`는 완전한 명시 values가 있을 때만 사용한다. live→source chart diff는 기본 거부하며 `--chart-diff-approved FILE` 내용이 현재 `chart-diff.txt`와 정확히 같아야 한다. server-side dry-run과 revision 재검사를 거친다.
- 고정 명령 phase는 `preflight`, `stage`, `quiesce`, `migrate`, `configure`, `accept`다. 명령은 `{argv: [...], seconds: 양의정수, source: 선택적 toolSources 키}`이며 shell 해석은 없다. 실제 검토한 script만 사용한다. 별도 버전 migration/tool은 `toolSources`의 commit과 `{tool:NAME}` 경로로 고정한다.
- `stage`는 evidence 상대 경로로 선언한 `generatedValues`를 생성한다. 입력은 최종 렌더와 quiesce **이전**에 동결한다. 후속 단계가 동결 입력/기록을 고쳐서는 안 된다. runtime/harness 활성화는 실제 declaration/new-input values와 stage/accept가 필요하다. FirstFloor 6종은 `runtimeDeclarations: true`의 고정 구현을 사용할 수 있다(inspection 기본 3600초, `runtimeInspectionSeconds`). configure나 runtime probe를 추측하지 않는다.
- 16개 source role과 플랫폼/빌드 인자는 `images.py` catalog 및 hack README에 명시되어 있다. 이미지는 자동 선택하지 않는다. Query는 server와 별도 image다. node-agent schema 변경 시 소유 operator도 검토한 명시 선택에 포함한다. runtime/harness 발행만으로 설치·실행 성공을 주장하지 않는다.
- prebuilt는 `images` 항목의 `{"role": "server", "prebuilt": "/absolute/private/publication.json"}`로 지정한다. 선택 repository의 image·artifact digest를 사용하고 registry에서 실제 artifact와 요청 platform config를 확인한다. 과거 sourceSHA·빌드 정보·OCI label은 기록으로 보존하며 서로의 일치를 요구하지 않는다. prebuilt를 재빌드하지 않으며 chart에는 digest로 고정한다.
- 바뀐 입력만 빌드하려면 `reusePublication`에 이전 `publication.json`을 지정한다. 원래 빌드의 receipt/`buildInputs`에 기록된 source·Dockerfile·COPY/ADD·Dockerignore와 새 source를 비교하며 실제 args·platform·고정 외부 source도 확인한다. 후속 배포 wrapper나 OCI label로 빌드 출처를 지어내지 않는다. 출처 부재·비교 불가·동적 입력은 보수적으로 빌드한다. `image-plan.json`과 CLI에 결정·사유를 남기며 inspect/execute는 registry artifact/platform을 재확인한다. broad COPY와 floating base/package 갱신 감지의 한계는 유지한다. 의도적 전체 갱신에는 reuse를 생략한다. exact-image probe·활성화는 생략하지 않는다. 발행 root sourceSHA는 배포 source, role sourceSHA는 기록된 빌드 출처이며 명시 prebuilt에서는 미기록일 수 있다.
- Pod spec의 선언 이미지/container name과 같은 이름의 status로 식별하고 container ready 및 Pod Ready를 확인한다. restartCount·imageID와 발행 digest 비교는 접수 조건이 아니다. CRI status의 image 문자열을 선언 이미지 대신 쓰지 않는다.
- serving-entrypoint의 readinessProbe는 실제 TLS listener와 같은 named port `https`의 `HTTPS /healthz`를 확인한다(초기 2초, 주기 5초). 느린 초기화를 재시작시키는 liveness/startup probe는 추가하지 않는다. kubelet HTTPS probe는 인증서를 검증하지 않지만 acceptance는 지정 CA/hostname 검증을 유지한다([Kubernetes probe semantics](https://kubernetes.io/docs/concepts/workloads/pods/probes/)). rollout/Pod 검사와 API 시작 대기는 하나의 `readinessSeconds` deadline을 공유한다. connection refused/reset/timeout 및 TLS handshake EOF 등 명시적 시작 transport 오류와 HTTP 503 `CoverageWarming`만 최대 5초 간격으로 GET 대기하며, coverage 성공 뒤 profiles transport 오류도 같은 deadline으로 대기한다. coverage가 warming이면 profiles를 조회하지 않는다. 각 GET은 최대 60초 및 남은 시간의 절반으로 제한한다. `after/attempt-NNN`의 원문 HTTP/body 및 `*.transport-error.json`에 오류 종류·errno·대기 가능 여부를 보존한다. 401/403, TLS 인증서·프로토콜 오류, 잘못된 JSON, 다른 503, snapshot/source 검증과 임의 오류는 즉시 중단한다. 이는 배포 재시도나 과거 성공 fallback이 아니다.
- API 환경 파일은 기존 `d2.apiBase`, `d2.caFile`, `d2.tokenFile`을 사용한다. CA/TLS/Bearer 인증과 redirect 금지를 유지하며 JWT exp·토큰 잔여 수명을 실행 예산과 비교하지 않는다. 원문 token은 로그/argv에 남기지 않는다. profile ID/수, 숫자 batch, usable coverage 및 측정 비교는 report-only이며 그 차이만으로 배포를 막지 않는다. `expectedProfileCount`·`expectedCoverageUsable`은 보고 목표다. `/api/v1/` 상대경로의 `apiChecks`는 HTTP 200과 응답 크기 `< maxBytes`를 요구한다.
- 새 private evidence에는 recipe/source, 명령 로그, live values/manifest/hooks, retained objects, publication, render/chart diff, before/after API, readiness, 실패 기록을 보존한다. 민감한 운영 데이터를 공개 저장소에 올리지 않는다. 실패 시 자동 restore/rollback/retry/resume은 없다. 결론과 증거 절대경로를 보고한다.
- 현재 첫 live 사용은 server/profiler/query/dashboard 및 FirstFloor 6종의 실제 dashboard 10-role 선택이다. FirstFloor build/publication → 별도 고정 승인 exact-image probe → 측정된 declaration activation을 모두 수행한다. 향후 미활성 발행에만 명시 `publicationOnly`를 사용하며 이번 활성화를 대체하지 않는다. 과학 결과는 report-only이며 실제 data 손실·보안·인증/토큰·CLI 오류 gate는 유지한다.
- `queryService: "chill-query"`는 private port-forward의 `/api/v1/scheduler/profiles?view=summary&limit=1`로 직접 401/인증 200을 확인한다. 인증 검사는 전체 catalog 크기에 의존하지 않으며 기존 8MiB fetch 상한은 유지한다. `dashboardCheck: true`는 HTTP 200 HTML만 확인하며 browser 시험이 아니다.
- accept의 평가 SA 권한 검사는 최종 `render-final.yaml`의 evaluation 템플릿(Source 주석) 및 evaluation 이름의 SA·binding subjects에서 대상을 찾고, 그 SA에 연결된 모든 렌더 Role/ClusterRole 규칙을 확장한다. 고정 SA·권한 목록은 없다. 이름이 중복된 binding 문서도 각각의 의도를 보존하여 실제로 유실된 권한을 검사한다. `kubectl auth can-i --as=system:serviceaccount:<namespace>:<sa>`와 SA의 실제 기본 그룹을 사용하며 RoleBinding namespace, ClusterRoleBinding의 전체 namespace, API group·resourceNames·subresource·nonResourceURLs를 구분한다. `evaluation-rbac.json`에 대상 SA·기대 권한·binding·검사 상태를 기록하고 `evaluation-rbac-NNNN.log`에 명령 stdout/stderr를 보존한다. 거부·명령 오류·참조 Role 누락은 STOP이며, 권한 생성이나 재시도는 없다. publish처럼 렌더된 grant가 없는 SA는 목록에만 남기고 권한을 지어내지 않는다. 렌더에 선언되지 않은 외부 SA는 이 검사의 대상이 아니다. kubectl의 can-i는 SelfSubjectAccessReview만 수행하며 실제 측정/Job은 시작하지 않는다([공식 CLI 계약](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_auth/kubectl_auth_can-i/)).
- `performance.json`·`command-timings.jsonl`에 단계/명령의 실제 시간을 보존한다. warming 동안 coverage를 profiles보다 먼저 확인하여 반복 profile 조회를 피한다. 측정하지 않은 전체 성능 향상을 주장하지 않는다.
- inspect/execute는 CLI watch를 자동 출력한다: 단계 BEGIN/DONE·단계/전체 경과, 이미지별 build/reuse 사유, 명령 시작/종료·5초 무출력 대기 heartbeat, 빌드/Helm 등의 로그, CoverageWarming 대기·잔여 deadline, COMPLETE/STOP와 증거 경로. argv/환경변수나 대용량 JSON/YAML은 터미널에 덤프하지 않으며 원본 로그·timing 파일은 그대로 남는다. 실행 중인 CLI에서 Ctrl-C는 현재 명령 process group을 정리하고 배포를 중단한다. 별도 tail viewer만 닫는 것과 구분한다.
- `baselineDirectory`로 원본 동결 baseline의 259 profiles 대비 손실 17을 보고할 수 있다. 현재 before 242와 after 진단은 별도로 보존하며 historical raw evidence를 고치지 않는다. `READY`는 operational Pod/API와 필수 단계 완료이지 experiment PASS가 아니다. 진단의 `passed: false`/errors도 숨기지 않는다.
- Default A는 `measuredAt` 최신 cell과 결정적 동률 처리를 사용하며 operator의 기존 replacement annotation을 우선한다. 이전 성공으로 fallback하지 않는다. 새 allow-flag 체계를 만들지 않는다.
- custom phase의 current context는 발행 전과 각 phase 전 recipe context와 같아야 한다. token-bearing kubeconfig를 flatten/capture하지 않는다.
- 원본 source chart drift의 `--chart-diff-approved`와 별개로 generatedValues 최종 렌더를 신뢰된 image overlay+검증된 built-in runtime probe 출력과 비교한다. 그 외 config 변경은 quiesce 전에 정확한 별도 `--staged-diff-approved FILE`/`stagedDiffApproved` 승인이 필요하다. 임의 override를 probe 출력으로 간주하지 않는다.
- strict current-before/after 기록과 원본 frozen 259/승인 손실 17의 four-criterion 보고를 분리한다. coverage 불명은 `null`/source-incomplete 진단이며 0으로 추측하거나 배포를 막지 않는다.
- tracked 첫 recipe는 `hack/deploy/plans/dashboard-reads-20261103.json`이다. application source는 1b81e0d5로 고정돼 있으며 승인된 `--source`로 명시 변경할 수 있다. main이 확인한 post-cutover revision 178·control-315를 기록하며, 이후 revision이 바뀌면 새 검토가 필요하다.
- `make test-deploy`는 저장소 회귀 진입점이며 `make test`에도 포함된다.
- CRD는 Helm/chart 단독 소유다. 별도 SSA apply/wait나 ownership patch를 하지 않는다. chart에서 제거할 CRD는 live CRD에 이미 `helm.sh/resource-policy: keep`가 있어야 한다(Helm 3.19.2 삭제 경로의 실제 조회 대상). 도구가 자동으로 keep를 추가하지 않는다.
- `checkConfiguration: true`는 선택 source의 실제 chill-server offline validator를 한 번 빌드하고 실제 flags/live JSON 또는 명시 `schedulerConfigMapFile`로 외부 scheduler ConfigMap을 검사한다. 첫 dashboard recipe에 활성화한다. 실제 config/CLI 오류는 blocking이며 과학 진단과 다르다. prebuilt round에는 기본 Go 빌드를 추가하지 않는다.
- `waitFor`에 Deployment/DaemonSet을 명시하면 readiness 예산 내 rollout status를 기다린다. 첫 recipe는 operator/profiler 및 DaemonSet 3개를 포함하며 실제 phase/command timing으로 대기 시간을 보고한다.
- 첫 전환의 `inspectionCells`는 개별 판정된 variant 배열, `inspectionRequests`는 함께 캡처한 native BEO다. inspect는 최신 native BEO·public profiles/coverage를 읽어 같은 생산 선택/판정 함수로 수치를 계산한다. native ID/실행 상태가 바뀌었으면 선택·예상 수치는 null/unavailable로 표시하며 과거 성공을 대신 보여 주거나 배포를 막지 않는다. annotation 변경은 기존 override 규칙대로 적용한다. 지정한 snapshot에 대한 조건부 예상이며 raw 재검증·하드웨어 동등성 증명이 아니다. 과거 근거·선언을 복제하지 않는다.
- preflight·upgrade 직전에 전체 namespace의 PR/BEO/MLIB/DOM/EvaluationRun 및 소유 native Job에 corrected zero-work gate를 적용한다. terminal DOM은 spec.conditions가 없어도 보존한다. 미완료·unfrozen·cleanup pending·삭제 중 root가 있으면 데이터 손실 방지를 위해 STOP하며 자동 drain/delete하지 않는다.
- runtime 선언을 활성화하면 source CPU/CLASSES/RKNN에서 실제 probe 대상을 도출하고 preflight·probe 직전에 Ready=True와 CUDA GPU allocatable≥1을 확인한다. source 1b81e0d5는 genesis2 CUDA를 제외한 14 declarations·8 target Nodes다. 자동 retry·노드 복구·외부 Pod 변경은 없다.

첫 live 사용은 배포 담당 세션 main 단독 실행만 허용한다.
여기의 절차나 회귀 검사는 live 승인 자체가 아니며 browser/hardware/campaign/과학
결과 성공을 의미하지 않는다. 로컬 회귀 명령(이 문서 수정 중 실행하지 않음):
`python3 -B hack/deploy/test_deploy.py`,
`python3 -B hack/deploy/test_acceptance.py`.

## 과거 E5 사전 점검 절차 기록

아래 명령·1620초 예산·PASS 기준은 이전 수동 E5 절차의 기록이다. 현재 `demo.py`는 오프라인 부하 미리보기만 제공하며 아래 preflight/report 명령을 실행하지 않는다. 현재 실행은 EvaluationRun/Stage와 기존 `evaluation.py` Job 경계를 사용한다. 과거 입력·승인·원시 증거는 유지한다.

1. 기존 `CHILL_EXPERIMENT_CONFIG`의 API/CA/tokenFile·operationalRecipe·campaign 경로와 라이브 제어 이미지 출처를 확인한다. `hack/evaluation/preflight-config.example.json`을 승인된 외부 입력으로 복사하고 `controlSourceSHA`를 실제 배포 소스로 고정한다(예시 SHA를 최신으로 간주하지 않는다). 기존 scheduler/instance를 건드리는 **별도 Main 승인**이 있어야 실행한다.
2. Capacity API는 자율 Workload의 행만 제공하므로 자율 off인 prepare 뒤에는 capacity 행을 기다리지 않는다. start에서 자율 on 뒤 E0 `readyGate`가 최대 720초 동안 canonical 3 Workload의 Ready + 양수 route를 기다린다. capacity 행 존재만으로 Built/Ready 성공을 판정하지 않는다.
3. 입력은 빈 출발(instances 0·예약 0), E0 Ready gate 최대 720초(세 canonical Workload 모두 Ready + 양수 route일 때 조기 종료), MIX 60초(모바일넷 20/s·Smol .3/s·PLC 40/s)다. `minimumDemand`도 20/.3/40으로 UID 재바인딩한다. 혼합 부하의 성공률은 성공 HTTP 200/전체 offered이며 SLO 충족률과 별개다.
4. Main의 기존 release JSON(`executor=CampaignOps`, `command=e5-execute`, `bindingPath=<OUT>/binding.json`, 실제 `sourceSHA`, `mainInstructionUTC`)과 새 OUT을 지정해 한 줄로 실행한다. prepare → start(자율 on) → E0 Ready gate → MIX(각 수집 포함) → finish → 소유 producer/정의 정리 → summary → 기존 `demo.py report` 경로가 연결된다. 새 빌드·실행 프레임워크는 없다.
사전 점검은 Ready → 부하 → 정리 연속성만 확인하므로 MIX에는 `startBarrier`를 두지 않으며, E0 이후 자율 재배치의 예약·전환 해제를 기다리지 않는다.

```sh
CHILL_EXPERIMENT_CONFIG="$ENV" python3 hack/evaluation/demo.py preflight --config "$CONFIG" --out "$OUT" --release "$RELEASE"
```

당시 예상 예산은 단계 780초 + 두 단계 각각 timeboxes 합 405초 + 여유 30초 = **1620초(27분)**였다. `collectSeconds=240`은 drain 후 power support +60초와 exact-hour archive 대기를 확보하기 위한 값이었다. 기존 120초는 실클러스터에서 `Finite collection deadline exhausted`로 실패했고 두 단계 수집 예산 때문에 기존 1500초 cap을 1620초로 올렸다. 이는 당시 단계/대기 예산이며 현재 토큰 TTL 접수 기준이 아니다.

`summary.json`의 `preflight.status`와 `summary.md`에 **PASS/FAIL**을 남긴다. PASS는 빈 출발, Ready 3/3, 혼합 창의 세 Workload 각각 offered>0·clientRows=offered·성공률 100%, 두 창의 offered/drain-inclusive 전력 `complete=true`, 모든 단계 완료, finish/정의 정리 완료, **최종 instances 0·예약 0**를 모두 요구한다. 누락/STOP은 FAIL(exit 2)이다. `report-tables.md`·`timeline.svg`는 같은 보존 폴더에서 생성하며 재생성만 할 때는 `python3 hack/evaluation/demo.py report "$OUT"`을 사용한다. FAIL이면 부분 증거를 보존하고 중단한다. 자동 재시도·자동 실패 reset은 없으며 정리도 별도 승인된 기존 finish 절차로 수행한다.

## 현재 인증·평가 입력 경계

배포와 평가 실행은 실제 API의 CA/TLS/Bearer 인증 결과를 따른다. JWT `exp`, `X-Token-Expires-At`, 예상 실행 예산의 TTL 비교는 시작 조건이 아니다. 토큰 신규 발급·TokenReview POST는 하지 않으며 원문 token은 증거·argv에 남기지 않는다. 인증 거부 401/403과 실제 CLI·전송 오류는 보존한다.

평가 prepare는 controller가 고정한 입력으로 generated·explicit arrival offsets를 한 번 동결한다. 두 종류 모두 기존 workspace PVC의 읽기 전용 파일을 producer에 전달하며 producer는 재생성하지 않는다. SHA/count는 기록이며 접수 관문이 아니다. 실제 숫자 입력·응답 품질·부분 결과와 exact-UID 소유 writer 정리 경계는 유지한다.

로컬 확인 진입점은 `python3 hack/evaluation/demo.py --self-check`(오프라인 미리보기), `python3 -B hack/deploy/test_deploy.py`, `python3 -B hack/deploy/test_acceptance.py`다.

## genesis1 디스크 압박과 CloudCore 배치 (2026-10-30)

### 확인한 관리 경계와 원인

이번 작업은 **조회와 문서 변경만** 수행했다. 아래 수동 변경·삭제·재시작 명령은 실행하지 않았으며, 별도 운영 승인 후 유지보수 창에서만 수행한다.

- 라이브 조회: `nice -n 19 /usr/bin/kubectl --kubeconfig /home/genesis1/.kube/config --context kubernetes-admin@kubernetes --request-timeout=30s -n kubeedge get deploy cloudcore -o yaml`. 고정 위치는 **`spec.template.spec.nodeName: genesis1`**이며 `nodeSelector`는 없다. Deployment는 `Recreate`, `hostNetwork: true`, 이미지 `kubeedge/cloudcore:v1.18.0`, Helm release `kubeedge/cloudcore`다.
- `helm -n kubeedge list`의 chart는 **`cloudcore-1.15.1`**, revision 1이다. chart/appVersion 1.15.1과 실제 이미지 v1.18.0을 혼동하여 chart를 올리지 않는다. 같은 kubeconfig/context로 `helm get values cloudcore -a -o yaml`을 읽으면 `cloudCore.nodeSelector: {}`다. `helm get manifest cloudcore`의 저장된 Deployment에는 `nodeName`도 `nodeSelector`도 없다. 따라서 라이브 `nodeName`은 저장된 Helm manifest와의 drift이며, 누가 언제 추가했는지는 이번 조회로 확인되지 않았다.
- 저장소 전체 `cloudcore|cloudCore|CloudCore` 및 `charts/`·manifest 이름 검색에서 CloudCore 배포 원본은 발견되지 않았다. 저장소 chart는 CHILL용 `charts/chill`뿐이다. `observerNodeName: genesis1`은 관측기 설정으로 CloudCore Pod 배치와 무관하므로 바꾸지 않는다. **외부 Helm release의 수동 절차를 기록하며 CHILL values에 무효한 설정을 추가하지 않는다.**
- 과거 사고 수치의 출처는 `docs/intent/decision-log.md`의 2026-10-29 기록(`agent://KubeedgeEvict`)이다: 디스크 압박 시 스케줄러를 우회하는 직접 `nodeName` 지정으로 CloudCore Pod **재생성 루프 2,747회**, PodGC 기본 임계 **12,500**까지 종료 Pod 누적. 이 수치는 과거 조사 결과이며 현재 Pod 수를 다시 센 값이 아니다. `nodeName`은 스케줄러의 배치 판단을 건너뛰지만 kubelet의 disk eviction을 막지 못한다. ReplicaSet이 퇴거된 Pod를 보충하면서 동일 노드에서 반복된다.
- 2026-10-30 조회 시 genesis1은 hostname label `genesis1`, taint 없음, Ready=True, DiskPressure=False다. 이는 압박 재발 위험이 없다는 뜻이 아니다. selector 전환 후에는 압박 시 Pending으로 남아야 하며, `node.kubernetes.io/disk-pressure`를 무조건 tolerate하거나 다시 `nodeName`으로 우회하지 않는다.

### 승인 후 수동 적용: Helm values + 라이브 drift 제거

1. 변경 직전 위 Deployment YAML, `helm get values cloudcore -o yaml`(사용자 values), `helm get values cloudcore -a -o yaml`, `helm get manifest cloudcore`, `helm history cloudcore`, genesis1 label/taint/조건을 접근 제한된 새 증거 디렉터리에 보존한다. kubeconfig/context는 위와 동일하게 명시한다. Helm manifest/values에는 민감 정보가 포함될 수 있으므로 공개 저장소에 올리지 않는다. CloudCore는 hostPath `/var/lib/kubeedge`와 hostNetwork에 의존하므로 다른 노드로 옮기지 않는다.
2. 외부 release 소유자가 기존 **cloudcore chart 1.15.1**의 신뢰할 수 있는 원본과 사용자 values를 확보한다. 원본을 확보하지 못하면 upgrade를 추측 실행하지 않는다. 기존 values를 유지하고 다음 overlay를 마지막에 합친다. 현 taint는 없으므로 새 toleration은 필요 없으며, 변경 시점의 정상 control-plane taint만 별도 검토한다.

   ```yaml
   cloudCore:
     nodeSelector:
       kubernetes.io/hostname: genesis1
   ```

3. 동일 chart·기존 사용자 values·위 overlay로 로컬 `helm template`을 생성해 **Deployment의 `spec.template.spec.nodeSelector`가 hostname 하나를 포함하고 `nodeName`은 없는지**, 이미지·affinity·서비스·인증서·나머지 자원이 바뀌지 않는지 검토한다. 그 입력 그대로 승인된 Helm upgrade를 수행한다. CHILL 배포 스크립트로 이 별도 release를 변경하지 않는다.
4. **Helm upgrade만으로 drift `nodeName`이 없어졌다고 가정하지 않는다.** Helm의 이전·새 manifest 양쪽에 없는 필드는 남을 수 있다. 조회해서 여전히 `nodeName: genesis1`이면 다음 JSON Patch로 비교 후 제거하고 selector를 같은 요청에서 보장한다. hostname이 달라졌거나 필드가 이미 없으면 `test`가 실패하므로 자동 재시도하지 않고 재조회한다.

   ```sh
   nice -n 19 /usr/bin/kubectl \
     --kubeconfig /home/genesis1/.kube/config --context kubernetes-admin@kubernetes \
     --request-timeout=30s -n kubeedge patch deployment cloudcore --type=json \
     -p='[{"op":"test","path":"/spec/template/spec/nodeName","value":"genesis1"},{"op":"remove","path":"/spec/template/spec/nodeName"},{"op":"add","path":"/spec/template/spec/nodeSelector","value":{"kubernetes.io/hostname":"genesis1"}}]'
   ```

5. `Recreate`라 중단 가능성을 사전 공지한다. 승인된 변경 뒤 `kubectl ... -n kubeedge rollout status deploy/cloudcore --timeout=180s`, Deployment template, 새 Pod 및 Events를 조회한다. template에 `nodeName` 없음·hostname selector 있음, 새 Pod Ready, 재시작/퇴거 증가 없음, Helm values에도 selector 유지가 완료 조건이다. **스케줄러가 배치한 실제 Pod의 `spec.nodeName: genesis1`은 정상**이다. 디스크 압박으로 Pending이면 공간을 복구하지 않고 우회 배치하지 않는다. 실패 시 원형을 보존하고 중단하며 자동 rollback/재시작하지 않는다.

### 여유 공간과 eviction / PodGC

- 운영 시작 전 `nice -n 19 df -B1 / /home /var/lib/kubelet /var/lib/containerd`로 확인한다. 2026-10-30 01:10 UTC 관측값은 동일 `/dev/nvme0n1p2` 파일시스템: 전체 **1,967,317,549,056 B**, 사용 **1,529,224,204,288 B**, 가용 **338,083,524,608 B**(338.1 GB / 314.9 GiB), 표시 사용률 82%다. **권고 여유 ≥400G 미달**이며 수집·빌드 증가량을 감안해야 한다. 여기서 운영 목표 400G는 보수적으로 **400 GiB = 429,496,729,600 B**로 정한다. 해당 스냅샷 기준 부족분은 약 **85.1 GiB**다.
- 같은 시점 `kubectl ... get --raw /api/v1/nodes/genesis1/proxy/configz`의 **실효** `evictionHard`는 `imagefs.available: 15%`, `nodefs.available: 10%`, `imagefs.inodesFree: 5%`, `nodefs.inodesFree: 5%`, `memory.available: 100Mi`다. 디스크 관련 15%가 모두 이미 적용됐다고 보고하지 않는다. `/var/lib/kubelet/config.yaml`에는 명시된 `evictionHard`가 없었다.
- 권고: 정리로 400 GiB 이상을 먼저 확보하고 유지보수 승인 후 **nodefs.available도 15%**로 상향한다(imagefs 15% 유지). 전체 `evictionHard` map에 위 memory/inode 값도 함께 유지한다. 일부 값만 쓰면 다른 기본 임계가 사라질 수 있다. kubelet 설정 백업→수정→승인된 kubelet 재시작 뒤 configz와 Node/Events를 읽어 실효값을 확인한다. 15%는 비상 퇴거 경계이지 운영 여유 목표가 아니며, inode도 별도로 `df -i`로 관측한다.
- 보조 권고: 종료 Pod 증거를 먼저 보존한 뒤 kube-controller-manager의 **`--terminated-pod-gc-threshold=1000`**을 초기 운영값으로 검토한다. genesis1의 `/etc/kubernetes/manifests/kube-controller-manager.yaml`에는 해당 flag가 없으므로 기본값 12,500을 사용한다고 해석한다. 이는 **클러스터 전체** 종료 Pod 수 임계이며 시간 기반 보존/로그 보존 정책이 아니다. 낮추면 실패 Pod의 API 증거가 더 일찍 사라진다. 승인 후 static Pod manifest를 백업·변경하고 재생성/leader 상태를 확인한다(백업 파일은 manifests 디렉터리 밖). 이는 API/etcd 누적 완화일 뿐 디스크 원인이나 재생성 루프를 고치지 못하므로 selector·공간 확보보다 앞세우지 않는다.

### 현재 du 트리 상위 10과 실제 회수 후보

2026-10-30 01시 UTC에 `nice -n 19 sudo -n du -x -B1 --max-depth=3 /home/genesis1 /var /tmp /opt /usr /root`로 읽었다. 아래는 **그 조회 범위의 트리 노드 중 스캔 시작점 자체를 제외한 상위 10개**다. 부모·자식이 겹치므로 **행을 합산하지 않는다**. `-x`는 각 시작점의 파일시스템만 순회하며 별도 Ceph/NFS/PVC 전체 용량은 포함하지 않는다. 최초 비특권 `/` 조회는 권한 오류(exit 1)로 `/var/lib/docker` 등을 누락했으므로 완전한 사용량 근거로 쓰지 않았다.

| 순위 | 경로 | 할당 바이트 (`du -B1`) | GiB | 판단 |
| --- | --- | ---: | ---: | --- |
| 1 | `/var/lib` | 872,077,946,880 | 812.19 | Docker·containerd·DB 등 혼합, 통째 삭제 금지 |
| 2 | `/var/lib/docker` | 843,639,705,600 | 785.70 | 최대 점유 원인; 이미지/빌더/영속 volume을 구분 |
| 3 | `/var/lib/docker/volumes` | 490,662,989,824 | 456.97 | BuildKit state와 registry·kind 데이터 혼합 |
| 4 | `/var/lib/docker/overlay2` | 352,536,936,448 | 328.33 | 이미지/컨테이너/빌드 레이어, 수동 삭제 금지 |
| 5 | `/home/genesis1/.cache` | 246,147,072,000 | 229.24 | 이름과 달리 대부분 실험 증거 |
| 6 | `/home/genesis1/.cache/chill-assets` | 218,461,147,136 | 203.46 | ops 증거·입력·venv 혼합 |
| 7 | `/home/genesis1/.cache/chill-assets/ops` | 210,476,130,304 | 196.02 | 실험/배포 원본 증거 우선 보존 |
| 8 | `/home/genesis1/ETRI` | 195,968,167,936 | 182.51 | 다른 연구자의 소스/결과 포함 |
| 9 | `/home/genesis1/ETRI/cheonbok` | 174,000,173,056 | 162.05 | CHILL 정리 권한 밖, 소유자 판단 필요 |
| 10 | `/home/genesis1/ETRI/cheonbok/Baseline_Framework` | 155,654,701,056 | 144.96 | 다른 연구의 결과/데이터일 수 있어 캐시로 간주 불가 |

부모 합계는 `/home/genesis1` 518.82 GiB, `/var` 813.86 GiB, `/opt` 35.45 GiB, `/usr` 18.56 GiB, `/root` 5.37 GiB, `/tmp` 1.70 GiB다. 별도 비특권 루트 트리에서 `/shared`는 30,419,435,520 B(28.33 GiB)였고 `experiment/data`가 있으므로 보존 대상이다. 추가 `du -x -B1 --max-depth=2 /home/genesis1/.cache/chill-assets /home/genesis1/ETRI/chill /var/lib/docker /var/lib/containerd` 조회에서 `ops/pools-deploy`는 **200,753,156,096 B(186.96 GiB)**였다. 시점이 다른 두 조회의 chill-assets 합계는 달랐다. [INFERENCE] 실시간 파일 변경 영향일 수 있으므로 합산하거나 정밀한 정적 스냅샷으로 주장하지 않는다.

읽기 전용 `docker system df` 결과:

| 종류 | 전체 / 활성 | SIZE | Docker 표시 RECLAIMABLE |
| --- | --- | --- | --- |
| Images | 355 / 9 | 232.7 GB | 230.7 GB (99%) |
| Containers | 14 / 10 | 4.278 MB | 1.36 MB (31%) |
| Local Volumes | 11 / 10 | 479.7 GB | 11.68 MB (0%) |
| Build Cache | 1615 / 0 | 106.9 GB | 106.9 GB |

이는 **Docker 회계**이며 `du`와 중복 레이어/단위가 달라 더하지 않는다. 이미지가 reclaimable이어도 운영 rollback/재현에 필요한 digest라면 보존한다. 먼저 검토할 후보는 **빌드 미사용 창의 106.9 GB build cache**이며 실제 회수량은 prune 후 `df`로만 판정한다. 그것만으로 400 GiB가 반드시 확보된다고 보장하지 않는다.

volume 상세 `du`는 `buildx_buildkit_chill-builder0_state` 92.87 GiB, `edge-builder0_state` 92.39 GiB, `merge-1132c500_state` 68.50 GiB다. 이들은 재생성 가능한 빌더 state 후보지만 **활성 volume 참조가 있고 Docker의 즉시 회수 가능 volume으로 잡히지 않는다**. 해당 builder 소유자·공유 잠금·진행 중 build 유무를 확인한 뒤 builder별 cache prune을 검토한다. `docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Mounts}}'`로 **`registry`(`registry:2`)가 쓰는 `f208fe69…` volume**도 확인했으며 해당 volume은 **95,379,914,752 B(88.83 GiB)**다. 이것은 배포 이미지 원본 저장소이지 캐시가 아니다. `df2229ec…`는 kind control-plane volume이다.

2026-10-31부터 `chill-builder`는 `hack/buildkitd.toml`(BuildKit 자체 GC, `maxUsedSpace = "20GB"`)로 생성한다. BuildKit이 빌드가 끝난 뒤 캐시를 20GB 목표로 회수한다(빌드 중 순간 사용량을 막는 하드 쿼터는 아니다). 따라서 위 chill-builder state 누적은 다시 생기지 않는다. builder를 다시 만들 때는 `docker buildx create --name chill-builder --driver docker-container --buildkitd-flags "--allow-insecure-entitlement=network.host" --buildkitd-config hack/buildkitd.toml`을 쓴다.

containerd는 20.11 GiB, kubelet 디렉터리는 0.01 GiB, `/var/log`는 1.32 GiB다. 즉 종료 Pod 누적 사고가 있었다고 해서 현재 최대 점유를 Pod 로그라고 단정할 수 없다. 사용자 캐시 중 `.cache/uv` 2.39 GiB, `go/pkg` 8.21 GiB, `.cache/go-build`는 약 21.2 MiB로 관측되어 Go build cache 정리만으로 부족분을 해결할 수 없다. inode는 `df -i`에서 **122,068,992개 중 10,728,424개 사용(9%)**이었다.

### 보존 규칙: 이름이 cache여도 실험 원본은 증거다

삭제는 **별도 승인된 경로만** 대상으로 한다. 이번 조사에서는 삭제/prune/GC/배포를 하지 않았다. 나이·크기만으로 자동 삭제하지 않으며, 공간 부족 시 새 대용량 빌드·측정을 보류하고 아래 재생성 캐시부터 검토한다.

| 경로/종류 | 보존 또는 삭제 가능한 시점 |
| --- | --- |
| `/home/genesis1/.cache/chill-assets/ops/pools-deploy/{harness,campaign,deployB,intent,...}` | **실험/배포 원본 증거**. 요청·응답/부하 로그, scheduler·전력 원시 행, progress, binding/source SHA, config·release, 단계 결과, 실패 원형, 이미지 digest·렌더 차이·명령 로그를 유지한다. PASS뿐 아니라 FAIL/중단 실행도 유지한다. 논문/보고서의 검토·재현이 끝나고 소유자가 보존 해제를 승인한 뒤, 독립 저장소 복제본의 파일 수·크기·SHA-256 및 원시 행으로 보고서 재생성이 확인된 디렉터리만 로컬 제거 가능하다. `/tmp`로 옮기는 것은 백업이 아니다. |
| `chill-assets/{plc,stage,smol-int8,...}`, `/shared/experiment/data`, 모델/데이터셋·quantization 산출물 | 재다운로드 URL/버전/라이선스·원본 hash 또는 독립 복제본이 확인되기 전에는 보존한다. `plc` 전달 parquet/docx, 유일한 모델/보정 입력을 cache로 오인하지 않는다. 임시 staging 복사본만 digest와 최종 배포/아카이브 일치 및 미사용을 확인한 뒤 승인 제거한다. |
| `pools-deploy`의 `*.tar`, `before`, rollback chart·스냅샷 | 백업도 자동 삭제 대상이 아니다. 운영 rollback 필요 기간 종료 + 독립 백업 검증 + 증거 참조 없음 + 소유자 승인을 모두 만족할 때만 특정 복제본을 제거한다. 과거 10/18·10/19 tar 4개 약 31 GB 삭제 기록은 당시 조치이지 현재 디렉터리 전체 삭제 허가가 아니다. |
| `/home/genesis1/.cache/go-build`, `/home/genesis1/go/pkg/mod` | **재생성 가능**: 공유 잠금을 잡고 실행 중 Go build/test가 없는 창에서 `go clean -cache`를 우선 검토한다. module cache는 `go.sum`·원본 접근성/사내 모듈 재취득이 보장될 때만 `go clean -modcache`; 오프라인 유일 사본이면 보존한다. 삭제 후 cold build 비용을 감안한다. |
| `/home/genesis1/.cache/{uv,pip}`, `.npm` cache, 재생성 가능한 venv | lock/requirements와 패키지 원본 접근성, 사용 중인 프로세스 없음 확인 후 각 도구의 cache clean/prune을 사용한다. 현재 운영 스크립트가 쓰는 `chill-assets/venv*`를 일괄 제거하지 않는다. |
| `/var/lib/docker/{buildkit,overlay2,volumes}`, `/var/lib/containerd` | 디렉터리 직접 삭제 금지. Docker와 Kubernetes containerd는 별도 회계다. Docker build cache는 `docker system df`·`docker buildx du`로 참조/회수 가능량을 확인하고 빌더 미사용 창에서 승인된 builder prune만 수행한다. `docker system prune -a --volumes` 금지. volume은 registry·DB·실험 저장소일 수 있어 cache로 분류하지 않는다. containerd snapshot/content는 kubelet/CRI의 정상 image GC에 맡기며 롤백 이미지·유일 레지스트리 blob을 보존한다. `du` 크기 전체가 회수 가능량은 아니다. |
| `/tmp`, 작업트리의 build/bin·`node_modules`·`.next`, 외부 작업트리 | 프로세스·열린 파일·worktree 참조·미커밋 파일·증거 참조를 먼저 확인한다. 실행 종료 및 원본/digest가 보존된 특정 throwaway 산출물만 승인 제거한다. 다른 연구자 작업, `.git`, 유일 소스, 현재 에이전트 로그를 임의로 치우지 않는다. |
| `/var/log/{pods,journal}`, `/var/lib/{kubelet,etcd,rook}`, 영속 volume | 로그는 사고 구간을 아카이브한 뒤 승인된 logrotate/journal retention으로만 줄인다. kubelet Pod/volume, etcd, Ceph/registry/DB 데이터는 **캐시 아님**; 수동 `rm` 금지. 종료 Pod API 삭제와 실제 로그·컨테이너/이미지 디스크 회수는 별개다. |

정리 전후 같은 파일시스템의 `df -B1`·`df -i`와 대상 `du`를 기록한다. 열린 채 삭제된 파일·예약 블록·mount·측정 중 변경 때문에 `du`와 `df`는 일치하지 않을 수 있다. 차이가 크면 `sudo lsof +L1`과 mount 경계를 조회하여 원인을 확인하지, 차이를 “삭제 가능한 캐시”로 환산하지 않는다. **≥400 GiB 회복 및 DiskPressure=False 안정화**를 확인하고 나서 새 작업을 시작한다.

## Shelly 노드 전원 복구 경로 (2026-11-01)
- rasp5=`shellyplugsg3-e4b063ddacc0`, jetson-nano=`shellyplusplugs-80646fd66914`; `~/ETRI/research/edge-metrics/charts/edge-metrics/values-shelly-gateway.yaml:26–31`의 매핑과 `exporter/API.md:560–562`의 노드별 전용 1:1 플러그 기록을 먼저 확인한다.
- 플러그들은 NAT `155.230.34.160`에서 genesis1(`155.230.35.213`)의 gateway `18765`로 outbound WebSocket 연결하며, HTTP `18766`은 `/devices`·`/prometheus?hostname=...` 조회만 제공한다. 제어 API·직접 RPC 주소는 확인되지 않아 이번에는 전원 조작·gateway 수정 없이 중단했다.
- 재투입은 직접 제어 경로 확보 후 GetStatus로 ID·전력을 대조하고 off→10초→on 순서로만 수행한다. 전후 응답은 운영 루트의 `deployB/node-recovery-20261101/`에 보존하고 최대 10분간 Node Ready·rasp5 unreachable taint 자동 해제·Nano actuator Ready를 확인한다.

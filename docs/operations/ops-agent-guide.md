# 운영 에이전트 가이드

- **증거 먼저:** 기존 `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy` 기록과 라이브 값의 출처를 확인한다. 명령·실패 원형을 보존하며 관측과 추론을 구분한다.
- **공용 잠금:** 배포/빌드는 `/tmp/chill-heavy.lock`의 배타 잠금을 공유한다. 점유 중이면 중단한다. 다른 운영자의 작업과 겹쳐 실행하지 않는다.
- **시간 상한:** 명령은 제한 시간 안에 종료한다. upgrade는 의존 패키지 조회당 120초, 빌드당 1800초, Helm 8분, 후속 Ready/API 확인 루프 전체 480초다. Pod 조회 timeout과 sleep도 남은 시간으로 제한하며 API 요청은 `min(10초, 남은 시간)`의 curl 전체 요청 제한을 쓴다. 기한이 지난 HTTP 200도 성공 처리하지 않는다. 시간 초과의 stdout/stderr도 보존한다. 실패 시 증거 경로를 보고하고 멈춘다.
- **금지:** 강제 삭제, finalizer 제거, 자동 재시도, 암묵적인 reset/rollback 금지. 실패 후 재실행은 원인을 검토하고 별도 control 번호로 명시적으로 수행한다.

## 업그레이드 전용 배포

```sh
hack/fast-deploy.sh upgrade --source <commit> --control <NNN> --dry-run
hack/fast-deploy.sh upgrade --source <commit> --control <NNN>
# 범위를 명시하려면 --images server,profiler,dashboard (부분집합 허용)
# 로컬 회귀 확인: 이미지별 변경 경로 선택 및 HTTP 200/남은 deadline gate
hack/fast-deploy.sh upgrade --source HEAD --control 0 --self-check
```

`--dry-run`은 고정 소스를 임시 디렉터리에 펼쳐 실제 Dockerfile의 Go build 대상과 `go list -deps` 의존 디렉터리를 출력한 뒤 로컬 계획만 표시한다. 클러스터·레지스트리 조회, 이미지 빌드, 잠금, 운영 증거 파일 변경은 하지 않는다. 임시 소스는 자동 삭제되며 Go 모듈 cache는 채워질 수 있다. 라이브 태그는 조회하지 않으므로 최종 이미지 선택은 실제 실행의 `source-diff.json`에서 확인한다. Python3, git, tar, 소스의 Go toolchain이 필요하며 실제 실행에는 추가로 PyYAML, curl, Docker buildx의 `chill-builder`, `/usr/local/bin/helm`, `/usr/bin/kubectl`이 필요하다. Docker는 기존 인증 설정을 사용한다. Helm과 kubectl 모두 `--kubeconfig /home/genesis1/.kube/config`와 `kubernetes-admin@kubernetes` context를 명시하므로 direnv의 개발용 `KUBECONFIG`에 의존하지 않는다. 대상은 `chill-system/chill`, 레지스트리는 `155.230.35.213:5000/chill`이다. control-178의 성공 방식대로 buildx output에 `registry.insecure=true`를 지정해 발행하고, manifest는 같은 레지스트리의 HTTP API로 조회해 digest를 확인한다.

소스는 `git archive`로 고정한다. auto는 라이브 Helm 이미지 태그 끝의 소스 SHA를 해석해 경로 차이를 비교한다. 해석 불가 시 추측하지 않고 중단한다. server는 Dockerfile의 `cmd/chill-server`, profiler는 **`cmd/profiler`와 `cmd/transition-observer` 두 바이너리**의 linux/amd64, CGO 비활성화 `go list -mod=readonly -deps` 결과 중 저장소 안의 패키지 디렉터리만 사용한다. 해당 디렉터리 바로 아래 변경 파일(`*_test.go` 제외), `go.mod`, `go.sum`, 해당 Dockerfile이 빌드 선택 근거다. 모든 `cmd/`, `internal/`, `api/` 하위 경로를 통째로 공유하지 않는다. dashboard는 `chill-dashboard/` 및 해당 Dockerfile을 사용한다. `source-diff.json`은 이미지별 이전 SHA, 실제 build 대상, 의존 디렉터리/공통 입력 파일 목록과 일치한 변경 경로를 보존한다. 제어 이미지 세 종류만 허용하며 runtime 7종은 빌드할 수 없다. 라이브 values 스냅샷 위에 변경 이미지 overlay만 적용한다. 소스 chart와 라이브 manifest가 다르거나 이미지 외 렌더 차이가 생기면 중단한다. registry digest → 렌더 객체 목록/차이 → server-side dry-run → upgrade → Pod Ready/재시작0/digest + 공개 API HTTP 200 순으로 확인한다. 실행 imageID는 발행 index digest 또는 해당 linux/amd64 child digest와 일치해야 한다. 기존 옵션 전용 reset-deploy 경로는 변경하지 않는다.

실제 upgrade 전에 CampaignOps와 같은 `CHILL_EXPERIMENT_CONFIG=/absolute/environment.json`을 설정한다. 기존 `hack/e5/d2/conduct_before.py`의 실제 호출 계약인 `d2.apiBase`(HTTPS origin), `d2.caFile`, `d2.tokenFile`을 그대로 사용한다. 새 토큰 발급이나 인증 우회 없이 해당 CA로 TLS/hostname을 검증하고 `Authorization: Bearer <파일 토큰>`으로 `GET /api/v1/scheduler/state`를 호출한다. redirect는 따라가지 않으며 **HTTP 200과 curl 성공 및 남은 deadline**이 모두 있어야 READY다. 연결 거부/비200은 480초 안에서 다시 관측한다. 토큰은 argv/증거 로그 대신 curl의 비공개 stdin으로만 전달한다. 이 API gate는 server를 이번에 빌드하지 않은 경우에도 적용된다. profiler/dashboard 자체의 앱 API gate는 없고 Pod Ready를 사용한다.

증거는 `O/deployB/control-<NNN>-<UTC date>/`의 `commands.log`, `source-diff.json`, `live-values.json`, `post-values.json`, `publication.json`, `render-diff.{json,txt}`, `server-dry-run.log`, `readiness.json`, `application-readiness.jsonl`에 남고 성공하면 `O/deploy-log.md`에 한 줄 추가한다. `readiness.json`은 Pod gate와 마지막 API status/exitCode/오류를 나눠 기록하고 `application-readiness.jsonl`은 관측 이력을 보존한다(응답 본문/토큰 미보존). 실패는 `failure.json`을 보존한다. **보고는 결론 한 줄 + 증거 절대 경로**로 끝낸다. 배포 명령 성공만으로 업무 시나리오의 성공을 주장하지 않는다.

## E5 각 실행 직전 표준 절차

E5보다 먼저 **최대 25분 사전 점검(Ready 대기 포함)**을 새 출력 디렉터리에서 수행한다. 이는 운영 입구 확인이지 연구 시나리오 PASS가 아니다. 현재처럼 클러스터 쓰기가 승인되지 않은 작업에서는 아래 라이브 명령을 실행하지 않는다.

1. 기존 `CHILL_EXPERIMENT_CONFIG`의 API/CA/tokenFile·operationalRecipe·campaign 경로와 라이브 제어 이미지 출처를 확인한다. `hack/e5/preflight-config.example.json`을 승인된 외부 입력으로 복사하고 `controlSourceSHA`를 실제 배포 소스로 고정한다(예시 SHA를 최신으로 간주하지 않는다). 기존 scheduler/instance를 건드리는 **별도 Main 승인**이 있어야 실행한다.
2. Capacity API는 자율 Workload의 행만 제공하므로 자율 off인 prepare 뒤에는 capacity 행을 기다리지 않는다. start에서 자율 on 뒤 E0 `readyGate`가 최대 720초 동안 canonical 3 Workload의 Ready + 양수 route를 기다린다. capacity 행 존재만으로 Built/Ready 성공을 판정하지 않는다.
3. 입력은 빈 출발(instances 0·예약 0), E0 Ready gate 최대 720초(세 canonical Workload 모두 Ready + 양수 route일 때 조기 종료), MIX 60초(모바일넷 20/s·Smol .3/s·PLC 40/s)다. `minimumDemand`도 20/.3/40으로 UID 재바인딩한다. 혼합 부하의 성공률은 성공 HTTP 200/전체 offered이며 SLO 충족률과 별개다.
4. Main의 기존 release JSON(`executor=CampaignOps`, `command=e5-execute`, `bindingPath=<OUT>/binding.json`, 실제 `sourceSHA`, `mainInstructionUTC`)과 새 OUT을 지정해 한 줄로 실행한다. prepare → start(자율 on) → E0 Ready gate → MIX(각 수집 포함) → finish → 소유 producer/정의 정리 → summary → 기존 `demo.py report` 경로가 연결된다. 새 빌드·실행 프레임워크는 없다.

```sh
CHILL_EXPERIMENT_CONFIG="$ENV" python3 hack/e5/demo.py preflight --config "$CONFIG" --out "$OUT" --release "$RELEASE"
```

예시 예상 예산은 단계 780초 + 두 단계 각각 timeboxes 합 285초 + 여유 30초 = **1380초(23분)**다. JSON의 `tokenSafetyMarginSeconds`로 여유를 고정하며 1500초(25분)를 넘는 사전 점검 설정은 거부한다. `collectSeconds=120`은 `e5-archive.py`가 요구하는 drain 후 power support +60초를 수집 deadline 안에 확보하기 위한 값이다. 이는 단계/대기 예산이며 prepare의 로컬 I/O와 오프라인 그래프 생성까지 포함한 강제 kill 타이머는 아니다. start에서 토큰 수명을 다시 검사한다. 시간 상한이나 수집 실패를 성공으로 바꾸지 않는다.

`summary.json`의 `preflight.status`와 `summary.md`에 **PASS/FAIL**을 남긴다. PASS는 빈 출발, Ready 3/3, 혼합 창의 세 Workload 각각 offered>0·clientRows=offered·성공률 100%, 두 창의 offered/drain-inclusive 전력 `complete=true`, 모든 단계 완료, finish/정의 정리 완료, **최종 instances 0·예약 0**를 모두 요구한다. 누락/STOP은 FAIL(exit 2)이다. `report-tables.md`·`timeline.svg`는 같은 보존 폴더에서 생성하며 재생성만 할 때는 `python3 hack/e5/demo.py report "$OUT"`을 사용한다. FAIL이면 부분 증거를 보존하고 중단한다. 자동 재시도·자동 실패 reset은 없으며 정리도 별도 승인된 기존 finish 절차로 수행한다.

**시작 전 토큰 만료 검사:** E5 `CPO.start/run`과 demo prepare/start/run은 같은 `hack/e5/auth.py`를 사용한다. 예상 실행 시간은 남은 단계 초 합 + 각 단계별 timeboxes 합 + `tokenSafetyMarginSeconds`(생략 시 60초)이며 campaign 시작은 정적 기준선/제어요소 단계도 포함한다. ServiceAccount JWT의 `exp`를 읽어 남은 시간이 예산보다 짧으면 API 변경/부하 시작 전에 거부한다. 이는 수명 검사이며 서명/권한 검증을 대체하지 않는다. JWT 형식이 아니거나 exp가 없으면 기존 CA·Bearer·redirect 금지로 scheduler state를 GET하여 **HTTP 200 + `X-Token-Expires-At`의 timezone 포함 시각**을 대안 증거로 요구한다. 서버가 만료 시각을 제공하지 않으면 추측하거나 무기한 토큰으로 처리하지 않고 거부한다. 토큰 신규 발급·TokenReview POST는 하지 않는다.

binding의 `config.tokenLifetime.expiresUTC`와 실행별 `token-preflight-*.json`에 검사 시각/만료 시각/남은 초/필요 초/출처만 기록한다(토큰 원문 미기록). upgrade도 이미지별 빌드 1800초 합 + Helm 510초 + readiness 480초 + `--token-safety-margin-seconds`(기본 120초)로 시작 검사하고 Helm 직전 재검사한다. `--images auto`는 세 이미지를 보수적으로 계산한다. `token-preflight.json`·`token-pre-upgrade.json`에 만료 증거를 남긴다.

로컬 회귀 확인(클러스터 접근/쓰기 없음): `python3 hack/e5/smoke_preflight.py`, 기존 전체 lifecycle 확인은 `python3 hack/e5/demo.py --self-check`, upgrade 확인은 `hack/fast-deploy.sh upgrade --source HEAD --control 0 --self-check`.

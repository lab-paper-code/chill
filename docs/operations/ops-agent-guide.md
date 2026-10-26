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

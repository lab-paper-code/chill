# 운영 에이전트 가이드

- **증거 먼저:** 기존 `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy` 기록과 라이브 값의 출처를 확인한다. 명령·실패 원형을 보존하며 관측과 추론을 구분한다.
- **공용 잠금:** 배포/빌드는 `/tmp/chill-heavy.lock`의 배타 잠금을 공유한다. 점유 중이면 중단한다. 다른 운영자의 작업과 겹쳐 실행하지 않는다.
- **시간 상한:** 명령은 제한 시간 안에 종료한다. upgrade는 의존 패키지 조회당 120초, 빌드당 1800초, Helm 8분, 후속 Ready/API 확인 루프 전체 480초다. Pod 조회 timeout과 sleep도 남은 시간으로 제한하며 API 요청은 `min(10초, 남은 시간)`의 curl 전체 요청 제한을 쓴다. 기한이 지난 HTTP 200도 성공 처리하지 않는다. 시간 초과의 stdout/stderr도 보존한다. 실패 시 증거 경로를 보고하고 멈춘다.
- **금지:** 강제 삭제, finalizer 제거, 자동 재시도, 암묵적인 reset/rollback 금지. 실패 후 재실행은 원인을 검토하고 별도 control 번호로 명시적으로 수행한다.
- **측정 progress 캡처:** CR 생성 직후 `python3 hack/measure/capture-progress.py --node NODE --measurement CR_NAME --output-dir /tmp/NEW --wait-for-pod 300 --capture-load`를 실행한다(또는 백그라운드 선실행 후 CR 생성). CR의 Workload 소유 Pod를 2초 간격으로 최대 300초 기다리고 첫 행부터 timestamped follow를 시작한 뒤 `READY`를 출력한다.

## 업그레이드 전용 배포

```sh
hack/fast-deploy.sh upgrade --source <commit> --control <NNN> --dry-run
hack/fast-deploy.sh upgrade --source <commit> --control <NNN>
# 범위를 명시하려면 --images server,node-agent,dashboard (profiler,operator,console도 허용)
# chart 변경은 사전 검토한 canonical live→source diff 파일과 정확히 같을 때만 승인
# --chart-diff-approved /absolute/approved-chart-diff.txt (기본값은 chart 변경 거부)
# 전용 raw 수집 SA는 기본 off; --values overlay로 servingEntrypoint.evidenceCollector.enabled=true
# 명시 --values도 chart diff의 정확일치 승인 범위에 포함되며 원본을 보존한다(SCH-67).
# node-agent config schema가 달라지면 DS 소유 operator도 같은 승인 묶음에서 배포한다.
# 승인된 chart diff가 있으면 이미지 변경0도 허용(이미지 재빌드 없는 values/RBAC 전환).
# 로컬 회귀 확인: 이미지별 변경 경로 선택 및 HTTP 200/남은 deadline gate
hack/fast-deploy.sh upgrade --source HEAD --control 0 --self-check
```

`--dry-run`은 고정 소스를 임시 디렉터리에 펼쳐 실제 Dockerfile의 Go build 대상과 `go list -deps` 의존 디렉터리를 출력한 뒤 로컬 계획만 표시한다. 클러스터·레지스트리 조회, 이미지 빌드, 잠금, 운영 증거 파일 변경은 하지 않는다. 임시 소스는 자동 삭제되며 Go 모듈 cache는 채워질 수 있다. 라이브 태그는 조회하지 않으므로 최종 이미지 선택은 실제 실행의 `source-diff.json`에서 확인한다. Python3, git, tar, 소스의 Go toolchain이 필요하며 실제 실행에는 추가로 PyYAML, curl, Docker buildx의 `chill-builder`, `/usr/local/bin/helm`, `/usr/bin/kubectl`이 필요하다. Docker는 기존 인증 설정을 사용한다. Helm과 kubectl 모두 `--kubeconfig /home/genesis1/.kube/config`와 `kubernetes-admin@kubernetes` context를 명시하므로 direnv의 개발용 `KUBECONFIG`에 의존하지 않는다. 대상은 `chill-system/chill`, 레지스트리는 `155.230.35.213:5000/chill`이다. control-178의 성공 방식대로 buildx output에 `registry.insecure=true`를 지정해 발행하고, manifest는 같은 레지스트리의 HTTP API로 조회해 digest를 확인한다.

소스는 `git archive`로 고정한다. auto는 라이브 Helm 이미지 태그 끝의 소스 SHA를 해석해 경로 차이를 비교한다. 해석 불가 시 추측하지 않고 중단한다. server는 Dockerfile의 `cmd/chill-server`, profiler는 **`cmd/profiler`와 `cmd/transition-observer` 두 바이너리**, node-agent는 `cmd/node-agent`, operator는 `cmd/main.go`, console은 `cmd/console`의 CGO 비활성화 `go list -mod=readonly -deps` 결과 중 저장소 안의 패키지 디렉터리만 사용한다. 해당 디렉터리 바로 아래 변경 파일(`*_test.go` 제외), `go.mod`, `go.sum`, 해당 Dockerfile이 빌드 선택 근거다. 모든 `cmd/`, `internal/`, `api/` 하위 경로를 통째로 공유하지 않는다. dashboard는 `chill-dashboard/` 및 해당 Dockerfile을 사용한다. `source-diff.json`은 이미지별 이전 SHA, 실제 build 대상, 의존 디렉터리/공통 입력 파일 목록과 일치한 변경 경로를 보존한다. 제어 이미지 여섯 종류만 허용하며 runtime 7종은 빌드할 수 없다. node-agent는 모든 Node를 위해 linux/amd64·linux/arm64를 함께 발행하고 나머지는 linux/amd64다. 라이브 values 위에 변경 이미지 overlay만 적용한다. chart 변경은 빌드 전에 canonical 객체 JSON의 live→source unified diff를 `chart-diff.txt`에 보존·표시하며, `--chart-diff-approved` 파일 내용이 정확히 일치할 때만 허용한다(기본 거부). 이미지 overlay가 소스 chart에 이미지 외 변경을 더하면 여전히 거부한다. 전체 live→upgrade `render-diff.{json,txt}`와 승인 원본도 보존한다. registry digest → 렌더 객체 목록/차이 → server-side dry-run → upgrade → Pod Ready/재시작0/digest 및 인증 API200 순으로 진행한다. 실행 imageID는 발행 index 또는 해당 플랫폼 child digest와 일치해야 한다. 기존 옵션 전용 reset-deploy 경로는 변경하지 않는다.

Console API는 ChillServer의 별도 프록시 upstream이다. Console 코드가 바뀌면 `--images console`을 포함해야 하며 server 이미지 갱신만으로 Console API가 갱신되지 않는다.

실제 upgrade 전에 CampaignOps와 같은 `CHILL_EXPERIMENT_CONFIG=/absolute/environment.json`을 설정한다. 기존 `hack/e5/d2/conduct_before.py`의 실제 호출 계약인 `d2.apiBase`(HTTPS origin), `d2.caFile`, `d2.tokenFile`을 그대로 사용한다. 새 토큰 발급이나 인증 우회 없이 해당 CA로 TLS/hostname을 검증하고 `Authorization: Bearer <파일 토큰>`으로 `GET /api/v1/scheduler/state`를 호출한다. redirect는 따라가지 않으며 **HTTP 200과 curl 성공 및 남은 deadline**이 모두 있어야 READY다. 연결 거부/비200은 480초 안에서 다시 관측한다. 토큰은 argv/증거 로그 대신 curl의 비공개 stdin으로만 전달한다. 이 API gate는 server를 이번에 빌드하지 않은 경우에도 적용된다. profiler/dashboard 자체의 앱 API gate는 없고 Pod Ready를 사용한다.

증거는 `O/deployB/control-<NNN>-<UTC date>/`의 `commands.log`, `source-diff.json`, `live-values.json`, `post-values.json`, `publication.json`, `render-diff.{json,txt}`, `server-dry-run.log`, `readiness.json`, `application-readiness.jsonl`에 남고 성공하면 `O/deploy-log.md`에 한 줄 추가한다. `readiness.json`은 Pod gate와 마지막 API status/exitCode/오류를 나눠 기록하고 `application-readiness.jsonl`은 관측 이력을 보존한다(응답 본문/토큰 미보존). 실패는 `failure.json`을 보존한다. **보고는 결론 한 줄 + 증거 절대 경로**로 끝낸다. 배포 명령 성공만으로 업무 시나리오의 성공을 주장하지 않는다.

## E5 각 실행 직전 표준 절차

E5보다 먼저 **최대 27분 사전 점검(Ready 대기 포함)**을 새 출력 디렉터리에서 수행한다. 이는 운영 입구 확인이지 연구 시나리오 PASS가 아니다. 현재처럼 클러스터 쓰기가 승인되지 않은 작업에서는 아래 라이브 명령을 실행하지 않는다.

1. 기존 `CHILL_EXPERIMENT_CONFIG`의 API/CA/tokenFile·operationalRecipe·campaign 경로와 라이브 제어 이미지 출처를 확인한다. `hack/e5/preflight-config.example.json`을 승인된 외부 입력으로 복사하고 `controlSourceSHA`를 실제 배포 소스로 고정한다(예시 SHA를 최신으로 간주하지 않는다). 기존 scheduler/instance를 건드리는 **별도 Main 승인**이 있어야 실행한다.
2. Capacity API는 자율 Workload의 행만 제공하므로 자율 off인 prepare 뒤에는 capacity 행을 기다리지 않는다. start에서 자율 on 뒤 E0 `readyGate`가 최대 720초 동안 canonical 3 Workload의 Ready + 양수 route를 기다린다. capacity 행 존재만으로 Built/Ready 성공을 판정하지 않는다.
3. 입력은 빈 출발(instances 0·예약 0), E0 Ready gate 최대 720초(세 canonical Workload 모두 Ready + 양수 route일 때 조기 종료), MIX 60초(모바일넷 20/s·Smol .3/s·PLC 40/s)다. `minimumDemand`도 20/.3/40으로 UID 재바인딩한다. 혼합 부하의 성공률은 성공 HTTP 200/전체 offered이며 SLO 충족률과 별개다.
4. Main의 기존 release JSON(`executor=CampaignOps`, `command=e5-execute`, `bindingPath=<OUT>/binding.json`, 실제 `sourceSHA`, `mainInstructionUTC`)과 새 OUT을 지정해 한 줄로 실행한다. prepare → start(자율 on) → E0 Ready gate → MIX(각 수집 포함) → finish → 소유 producer/정의 정리 → summary → 기존 `demo.py report` 경로가 연결된다. 새 빌드·실행 프레임워크는 없다.
사전 점검은 Ready → 부하 → 정리 연속성만 확인하므로 MIX에는 `startBarrier`를 두지 않으며, E0 이후 자율 재배치의 예약·전환 해제를 기다리지 않는다.

```sh
CHILL_EXPERIMENT_CONFIG="$ENV" python3 hack/e5/demo.py preflight --config "$CONFIG" --out "$OUT" --release "$RELEASE"
```

예시 예상 예산은 단계 780초 + 두 단계 각각 timeboxes 합 405초 + 여유 30초 = **1620초(27분)**다. JSON의 `tokenSafetyMarginSeconds`로 여유를 고정하며 1620초(27분)를 넘는 사전 점검 설정은 거부한다. `collectSeconds=240`은 `e5-archive.py`가 요구하는 drain 후 power support +60초와 exact-hour archive 대기를 수집 deadline 안에 확보하기 위한 값이다. 기존 120초는 실클러스터에서 `Finite collection deadline exhausted`로 실패했으므로 시연/시험 설정도 240초를 사용한다. 늘어난 두 단계 수집 예산으로 기존 1500초 cap을 초과하여 필요한 최소인 1620초로 올렸다. 이는 단계/대기 예산이며 prepare의 로컬 I/O와 오프라인 그래프 생성까지 포함한 강제 kill 타이머는 아니다. start에서 토큰 수명을 다시 검사한다. 시간 상한이나 수집 실패를 성공으로 바꾸지 않는다.

`summary.json`의 `preflight.status`와 `summary.md`에 **PASS/FAIL**을 남긴다. PASS는 빈 출발, Ready 3/3, 혼합 창의 세 Workload 각각 offered>0·clientRows=offered·성공률 100%, 두 창의 offered/drain-inclusive 전력 `complete=true`, 모든 단계 완료, finish/정의 정리 완료, **최종 instances 0·예약 0**를 모두 요구한다. 누락/STOP은 FAIL(exit 2)이다. `report-tables.md`·`timeline.svg`는 같은 보존 폴더에서 생성하며 재생성만 할 때는 `python3 hack/e5/demo.py report "$OUT"`을 사용한다. FAIL이면 부분 증거를 보존하고 중단한다. 자동 재시도·자동 실패 reset은 없으며 정리도 별도 승인된 기존 finish 절차로 수행한다.

**시작 전 토큰 만료 검사:** E5 `CPO.start/run`과 demo prepare/start/run은 같은 `hack/e5/auth.py`를 사용한다. 예상 실행 시간은 남은 단계 초 합 + 각 단계별 timeboxes 합 + `tokenSafetyMarginSeconds`(생략 시 60초)이며 campaign 시작은 정적 기준선/제어요소 단계도 포함한다. ServiceAccount JWT의 `exp`를 읽어 남은 시간이 예산보다 짧으면 API 변경/부하 시작 전에 거부한다. 이는 수명 검사이며 서명/권한 검증을 대체하지 않는다. JWT 형식이 아니거나 exp가 없으면 기존 CA·Bearer·redirect 금지로 scheduler state를 GET하여 **HTTP 200 + `X-Token-Expires-At`의 timezone 포함 시각**을 대안 증거로 요구한다. 서버가 만료 시각을 제공하지 않으면 추측하거나 무기한 토큰으로 처리하지 않고 거부한다. 토큰 신규 발급·TokenReview POST는 하지 않는다.

binding의 `config.tokenLifetime.expiresUTC`와 실행별 `token-preflight-*.json`에 검사 시각/만료 시각/남은 초/필요 초/출처만 기록한다(토큰 원문 미기록). upgrade도 이미지별 빌드 1800초 합 + Helm 510초 + readiness 480초 + `--token-safety-margin-seconds`(기본 120초)로 시작 검사하고 Helm 직전 재검사한다. `--images auto`는 세 이미지를 보수적으로 계산한다. `token-preflight.json`·`token-pre-upgrade.json`에 만료 증거를 남긴다.

로컬 회귀 확인(클러스터 접근/쓰기 없음): `python3 hack/e5/smoke_preflight.py`, 기존 전체 lifecycle 확인은 `python3 hack/e5/demo.py --self-check`, upgrade 확인은 `hack/fast-deploy.sh upgrade --source HEAD --control 0 --self-check`.

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

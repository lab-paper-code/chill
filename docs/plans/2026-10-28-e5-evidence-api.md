# E5 증거를 CHILL API로: 우회 수집 제거 계획 (2026-10-28)

상태: 계획. 사용자가 범위를 확정했고(2026-10-28) 2차 검토를 반영했다. 2차 검토는 같은 날 fusion으로 했다: 패널 3, 판정 1, 판정 결과 "수정 후 진행". 적용 시점은 진행 중인 E5(#213)와 결과 표 기입이 끝난 뒤다. 그때까지 `hack/e5`와 `load-generator`는 동결한다.

## 1. 목표와 범위

다음 E5부터 하네스는 **CHILL 내부 사실을 CHILL API로만** 가져온다. CHILL 구성요소를 대상으로 하는 다음 우회 수집은 모두 없앤다: `sudo`+`crictl`+`/proc/<pid>/root` 읽기, S3 직접 다운로드, Node별 SSH cgroup 수집기, `kubectl` 스냅샷·로그·ConfigMap 읽기, 별도 감시 로그 복사.

| 구분 | 출처 | 이 계획에서 |
|---|---|---|
| CHILL 내부 사실: 요청 수신·결과·실행, 스케줄러 결정·스냅샷·동작·예약·경로, 정의·카탈로그, CHILL 인스턴스와 CHILL 제어 Pod의 자원 사용, 서빙 Pod 상태 변화, 장애 감지·격리·대체·복구, 서버가 수락한 전력계 선언 | CHILL | **API로 제공** |
| 실제 전력 | edge-metrics API | 범위 밖(이미 API가 있음). 단 "어느 전력계가 어느 Node인가", "어느 Node가 POOL인가"는 CHILL 사실이므로 API로 제공 |
| 클라이언트가 본 요청 결과·송신 시각, 부하 생성기 컨테이너 자원, 생산자 해제 절차 | 부하 생성기·하네스 | 범위 밖. **현재 수집과 절차를 유지**한다 |
| 실행 중 안전 감시(`control-alerts` STOP 루프) | 하네스 외부 감시 | 범위 밖. **운영 안전 장비로 유지**한다. 사후 증거만 `cluster`로 옮긴다 |
| 장애 주입 명령(E5-7) | 운영자 kubectl | 범위 밖. CHILL은 **감지와 대응만** 기록한다(사용자 결정 ①) |

## 2. 현재 우회 수집과 대체

근거는 인벤토리(2026-10-28)와 2차 검토다. 약어: E=`hack/e5/e5.py`, A=`hack/e5/e5-archive.py`, W=`hack/e5/e5-wire.py`.

| 현재 증거 | 지금 수집 방법 | 대체 |
|---|---|---|
| `obs/<hour>/{requests,scheduler,catalog,management}.jsonl.gz`, `obs/definitions.jsonl` | 현재 시간은 sudo→crictl→`/proc/<pid>/root`로 직접 읽음(A:33-103). 지난 시간은 `aws s3 cp`(A:249-276) | **API-1 원시 내보내기** |
| `raw-source-index.json` | 위 수집기가 생성. `e5-raw.py`의 **입력 목록**이다(`e5-raw.py:119-188`) | **유지.** API 수집기가 생성하고 origin은 `api`. 판정이 이 파일을 읽으므로 삭제 대상이 아니다 |
| `<node>-cgroup.jsonl`의 server·serving instance·edgemesh 행, thermal 행 | SSH로 각 Node에서 sudo crictl→`/proc/<pid>/cgroup`→sysfs, 1초(`hack/e5/d2/sample_cgroups.py:38-94`) | **새 관측 종류 `resources`**(node-agent). 서버 컨테이너도 genesis1의 node-agent가 잰다 |
| `<node>-cgroup.jsonl`의 `client` 행 | 같은 수집기 | **유지.** 시작 관문(E:1174-1180)과 생산자 해제(E:1163-1172)가 이 행을 쓴다. 수집기는 `client` 역할만 남긴다 |
| `cgmon.log`(게이트웨이 CPU·throttle·메모리) | 별도 감시 로그 통째 복사(E:1313-1315) | `resources`(genesis1 node-agent의 서버 컨테이너 표본). 복사는 삭제 |
| `server.log`의 GC CPU | `kubectl logs` + gctrace 파싱(E:1316-1319, 1624-1632) | 서버 in-process 값: Go `runtime/metrics` GC 누적 CPU와 사이클 수. **의미가 바뀐다**(5장). W:195-197의 gctrace 필수 조건은 삭제 |
| `server.log`의 observer 오류(native source issue 등) | 같은 로그 | `cluster`의 observer issue 행. E5-7의 "CHILL이 언제 감지했나"는 지금 이 로그에만 있다 |
| `pre/post-pvc-usage.json`(관측 디렉터리 바이트) | sudo+`/proc` 읽기(E:885-893) | 서버 in-process 값(분 1회) |
| `pre/post-pods.json` | `kubectl get pods -A`(E:873-884) | `cluster` Pod 행 |
| `pre/post-nodes.json` | `kubectl get nodes` | Node name·UID·Ready는 기존 scheduler 상태(`GET /api/v1/scheduler/state`의 snapshot.nodes)로 대체. 새 기록은 없다 |
| 제어 이미지 digest 확인 | `kubectl get pods -l component=serving-entrypoint`(E:885-889) | `cluster` Pod 행의 imageID |
| `control-alerts.jsonl` | 별도 감시 프로세스. 실행 중 즉시 STOP 입력(E:981-992, 1073-1075) | **감시 루프는 유지**(1장). 사후 재시작·교체 사실은 `cluster` |
| E5-7 대응(격리·대체·Ready·경로 회복) | 운영자 readback 파일 참조(E:190-215, 선택 입력) | `scheduler` + `cluster` 기록. 운영자 파일에는 **명령 사실만** 남긴다 |
| 전력계↔Node 매핑 `metric-sources.yaml` | `kubectl get configmap chill-profiler-facts`(A:578-590) | **settings 응답에 서버가 수락한 전력계 선언 노출**(3장). 현재는 `json:"-"`(`cmd/chill-server/scheduler_service.go:42`) |
| 전력 관측 정책 | `kubectl get configmap serving-scheduler`(E:626-629) | 이미 있는 `GET /api/v1/scheduler/settings`의 `powerObservation`(E:849) |
| 전력 reducer의 정규 Node 목록 | `pre-nodes.json` NodeList를 `--nodes`로 넘김(A:279-297, 449-455; `hack/observations/power/main.go:65-99`) | 기존 state의 Node 참조. reducer의 `--nodes` NodeList 전제를 바꾼다. **NodeList를 합성하지 않는다** |
| 서버 컨테이너 args 추출(관측 디렉터리, ConfigMap 이름) | `kubectl get pod`(W:192-197, E:1869-1870) | 불필요. 출처가 API가 되면 경로를 알 필요가 없다 |
| `capture-identity.json`, `s3-hour-list-*`, `upload.log`, `raw-copy.stderr`, `collector-status.json`, `*-collector.stderr` | 위 우회 경로의 수집 영수증 | **삭제.** 이 파일들을 읽는 판정 코드(E:1558, 1639)도 함께 정리한다 |
| `pre/post-{state,pools,settings}.json`, 경계·barrier·static 상태 | 이미 CHILL API(E:845-874) | 유지 |

## 3. 추가할 것 (최소 단위)

새 것은 다섯 가지다.
- **공개 엔드포인트 1개**: 원시 내보내기
- **내부 수신 엔드포인트 1개**: node-agent 표본
- **settings 응답 필드 1개**: 수락한 전력계 선언
- **새 관측 종류 2개**: `resources`, `cluster`

기록은 이미 있는 ObservationLog를 쓴다. 분 단위 세그먼트이고, 시간 단위 불변 S3 보관이 붙어 있다(`internal/serving/observation_log.go:804-835`). 읽기는 이미 있는 **파일 확보·출처 선택·보관본 열기**를 재사용한다(`observation_log_read.go:101-178`, `observation_log_capacity.go:16-20`). 과거 조회의 해석·재직렬화 경로(`observation_log.go:1333-1346`)는 쓰지 않는다.

### API-1 원시 내보내기

- `GET /api/v1/observations/raw?hour=YYYYMMDDTHH`
  - 그 시간의 종류별 분 세그먼트 목록과 시간 상태를 준다.
  - 시간 상태는 `open`, `closed-local`, `archived` 세 값이다. 보관은 다음 분 tick의 Flush에서 하고, 로컬 분 파일은 보존 기간(운영 2시간) 동안 남는다. 그래서 "시간이 닫혔다"가 "보관본이 있다"를 뜻하지 않는다.
- `GET /api/v1/observations/raw/{hour}/{kind}`
  - **로컬 닫힌 분 파일이 있으면 원래 JSONL 바이트를 그대로** 이어서 보낸다.
  - 로컬에 없으면(보존 기간 경과) **보관본 gzip을 그대로** 보낸다.
  - 두 표현은 `Content-Encoding`으로 구분한다. **서버가 즉석에서 압축하지 않는다**(서버 CPU 오염 방지).
  - `kind`: `requests`, `scheduler`, `catalog`, `management`, `resources`, `cluster`.
- `GET /api/v1/observations/raw/definitions`
  - 루트 파일이며 시간별 보관 대상이 아니다(`observation_log.go:26-36, 348-363`).
  - 잠금 아래 확정한 마지막 완결 행까지만 보낸다.
- 열린(현재) 분은 내보내지 않는다. 하네스는 단계 drain 뒤 다음 분 경계까지 기다렸다가 가져온다. 이미 받은 닫힌 시간은 다시 받지 않는다.
- **HTTP 마감을 명시한다.** 현재 GET은 요청 시간 제한 아래에서 돈다(`httpapi/handler.go:132-156`; CLI 기본 30초, 운영 차트 129초). 원시 경로는 `longOperation`(`operations.go:15-17`)처럼 처리 마감과 쓰기 마감을 따로 둔다.
- 크기 참고(E5-1, `20261028T01` 원문): scheduler 382 MB, requests 175 MB. 전송은 drain 뒤, 측정 창 밖에서만 한다. 이 전송은 서버 컨테이너 CPU·I/O에 잡힌다. 이전에는 호스트 sudo 프로세스가 읽었으므로 서버에 잡히지 않았다.
- 인증은 기존 TokenReview/SAR(`httpapi/handler.go:159-169`)를 쓴다. SAR에는 실제 요청 경로가 들어가므로(`kube/authorizer.go:135-142`) ClusterRole nonResourceURLs에 `/chill/api/v1/observations/raw`와 `/chill/api/v1/observations/raw/*`를 **둘 다** 넣는다. 요청 ID가 포함된 원시라서 별도 권한으로 둔다.
- 가공 API(`/observations/workloads`, `/scheduler`)는 대시보드용으로 그대로 둔다.

### 관측 종류 `resources`

**node-agent 표본(모든 Node, genesis1 포함)**
- 대상: CHILL 서빙 인스턴스 컨테이너와 CHILL 제어 Pod 컨테이너(서버 포함).
- 값: cgroup 원시 counter(CPU ns, throttle 횟수·시간, 메모리)와 thermal zone 온도(센서 경로별. D6가 센서별 값과 직전 표본을 쓴다, E:1559-1587).
- 행 필드: `node`, `podUID`, `containerID`, `cgroupVersion`, counter, 표본 시각.
- 재시작·종료 상태는 넣지 않는다(`cluster`가 기록). 그래서 node-agent에 Pod RBAC를 추가하지 않는다.
- 부하 생성기 컨테이너는 CHILL 사실이 아니므로 대상이 아니다(2장 `client` 행 유지).

**컨테이너 발견**
- crictl 없이 호스트 `/proc`·`/sys`(이미 마운트됨, `charts/chill/values.yaml:918-927`)에서 Pod UID·containerID로 cgroup 디렉터리를 찾는다.
- 실제 환경은 섞여 있다. genesis1·genesis3·Jetson 3대는 cgroup v1, rasp5·orangepi·lattepanda는 v2다. systemd slice, cgroupfs, jetsonx의 `/system.slice/containerd.service/...` 경로 형식도 섞여 있다(`control-213-full/full-E5-1/*-cgroup.jsonl`). 그래서 경로 모양을 추측하지 않는다.
- v1 파일: `cpuacct.usage`, `cpu.stat`의 `throttled_time`, `memory.usage_in_bytes`.
- v2 파일: `cpu.stat`의 `usage_usec`/`throttled_usec`, `memory.current`.

**전송(push)**
- 현재 node-agent에는 서버로 가는 경로가 없다(`cmd/node-agent/main.go:58-63`). 내부 수신 POST 하나를 둔다(예: `/api/v1/observations/resources`).
- 인증은 node-agent SA 토큰과 기존 TokenReview/SAR를 재사용하고, 그 경로를 node-agent ClusterRole에 추가한다.
- 표본 주기는 설정값이다(기본 1초). 전송은 묶음(예: 10초)으로 하고, 요청 1건을 Node당 1행(표본 배열)으로 기록해 append 횟수를 줄인다. 모든 행이 요청 경로와 같은 잠금 아래 기록되기 때문이다(`observation_log.go:519-533`).
- 영속 큐나 재전송 장치는 없다. 전송 실패 구간은 결손으로 남긴다.
- 표본 시각과 저장 시각이 다르다. 그래서 하네스는 "창 끝 + 묶음 주기 + 다음 분 경계" 뒤에 수집한다.

**서버 in-process 값(서버 자기 cgroup 읽기는 하지 않음)**
- Go `runtime/metrics`의 GC 누적 CPU(`/cpu/classes/gc/total:cpu-seconds`)와 사이클 수(`/gc/cycles/total`). 기존 1초 heap ticker(`internal/serving/overload.go:25-36`)에 얹는다.
- 관측 디렉터리 사용 바이트. 분 1회만 기록한다.

### 관측 종류 `cluster`

- **Pod**: 서버 observer는 serving namespace의 Pod를 watch한다(`kube/observer.go:201-242`). 다만 지금은 native source 중심으로만 처리한다(`:843-869`). 여기에 CHILL 서빙 인스턴스와 CHILL 제어 Pod의 초기 상태와 변화를 기록한다.
  - 필드: 생성·삭제, Pod UID, Node, containerID, imageID, phase·Ready, restartCount, 마지막 종료 사유와 exit code.
  - 서버는 기동할 때 자기 Pod UID와 이미지를 1행 기록한다. 그래야 서버 교체를 사후에 식별할 수 있다.
- **observer issue**: 발생·변경·해소를 기록한다(source, 사유, 시각). 지금은 로그에만 남고, 정상화되면 지워지며 `At`이 갱신된다(`observer.go:150-161`).
- **Node**: Node는 watch가 아니라 refresh List로 읽는다(운영 2초, `values-operating.yaml:586`). Node name·UID·Ready는 이미 scheduler 행에 있으므로(`scheduler.go:115-121, 279-294`) 따로 기록하지 않는다. `unschedulable`(cordon)은 CHILL 서빙이 읽지 않는 값이라 기록하지 않는다. cordon 사실은 운영자 명령 기록이다.
- 기록은 observer 잠금 밖에서 한다.

### settings 응답 필드

- `GET /api/v1/scheduler/settings`에 서버가 수락한 전력계 선언(`Meters`)을 **읽기 전용**으로 노출한다. 서버는 이미 ConfigMap과 Node UID로 이 값을 도출한다(`cmd/chill-server/scheduler_service.go:310-340`).
- 전력 reducer는 이 값과 state의 Node 참조만 입력으로 받는다.
- **쓰기 클라이언트 호환**: settings를 GET해서 고친 뒤 PUT하는 클라이언트가 둘 있다. 둘 다 읽기 전용 필드를 하나씩 지운 뒤 보내고, 서버는 모르는 필드를 거부한다(`httpapi/handler.go:172-175`). 그래서 `meters`를 노출하는 같은 변경에서 두 클라이언트가 `meters`도 빼고 보내도록 고친다. 서버의 엄격한 decode는 바꾸지 않는다.
  - 하네스 `CPO.put`(E:847-850): `staticRouting*`, `powerObservation`을 지운다.
  - 대시보드 `putSchedulerSettings`(`chill-dashboard/features/scheduler-settings.tsx:78-83`): routing 필드 두 개를 지운다.

## 4. 순서 (spec → SRS → 설계 → 구현)

| 단계 | 할 일 | 산출 |
|---|---|---|
| spec | `docs/spec/00_measurement.md`의 "Serving 관측" 행(18행)에 다음을 추가: "CHILL은 서빙 관측, CHILL 구성요소와 인스턴스의 자원 사용, 서빙 Pod 상태 변화와 장애 감지를 기록하고 원래 기록 그대로 API로 제공한다" | 한 커밋 |
| SRS | scheduler SRS에 요구사항 4개(원시 내보내기, `resources`, `cluster`, 전력계 선언 노출)와 수용 시나리오 | SCH-67~ / SCH-AC-52~ |
| 설계 | `docs/design/serving/observability-split.md`에 두 종류의 행 형식·주기·크기, 내보내기 의미(로컬 우선, 바이트 보존, 마감), node-agent 발견·전송. **ADR 0030**: 같은 prefix(`v1alpha5`)에 stream 2개를 추가하는 변경이며 버전은 올리지 않는다. 배포 이전 시간에 새 stream이 없는 것은 하네스 `absentStreams`로 처리하고 실제 결손과 구분한다. 다중 버전 reader는 만들지 않는다. ADR 0029는 보류(Deferred)이므로 이번 작업과 묶지 않는다. 이번 저장 계약 변경은 단일 gateway의 `resources`/`cluster` 추가로 한정한다. ADR 0019 일정 줄에 추가한다 | 한 커밋 |
| 구현 1 | API-1, settings 필드. 두 stream 이름을 `observationStreams`(`observation_log.go:36`)에 **한 번에** 등록한다 | 서버 |
| 구현 2 | `resources`: node-agent 발견·표본·묶음 POST, 서버 수신·기록, 서버 GC·디렉터리 바이트 | 서버, node-agent |
| 구현 3 | `cluster`: observer의 Pod 사건과 issue 전이 기록 | 서버 |
| 차트·배포 | ClusterRole: 하네스 principal에 `/raw`·`/raw/*`, node-agent에 수신 경로. 주기 값을 설정한다. stream 추가와 함께 **한 번에** 적용한다. 같은 승인 배포에 [대시보드 계획](2026-10-26-dashboard-live-stage.md) 5장 항목을 묶는다: 라이브 RBAC(`/chill/api/v1/live`), 대시보드 호출자(`serving-manager`)의 experiments·측정 projection 권한, 대시보드·서버 이미지. 라이브 적용은 사용자 승인을 받는다 | 차트 |
| 하네스 전환 | API로 교체하는 것: A의 host·S3 경로, cgmon·control-alerts 증거 복사, kubectl pods/nodes/logs/ConfigMap 읽기, pvc 읽기, W:192-197의 서버 args 추출과 gctrace 요구, E:1867-1874의 preflight host 사용량, 전력 reducer `--nodes`. 남기는 것: `sample_cgroups.py`의 `client` 역할만, `control-alerts` STOP 루프. `demo.py` 픽스처와 D2 `conduct_before.py`가 같은 파일을 공유하므로 영향 범위를 함께 정한다. 수집 영수증 파일은 삭제한다 | `hack/e5` 순감소 |
| 확인 | 리허설 1회(E5-0 + 혼합 60초)에서 **옛 수집과 새 수집을 동시에** 돌린다. 추가로 #213의 보관된 시간 하나를 API로 받아 판정까지 돌린다(보관본 경로 확인). 비교 기준은 아래와 같고, 확인 뒤 옛 경로를 삭제한다. 이 비교는 1회이며 상시 검증이 아니다 | 비교 1회 |

확인 단계의 비교 기준:
- 관측 stream에서 나오는 KPI(requests D1, 지연 통계, 전력 Joules)는 같아야 한다.
- `resources`는 값 일치 대신 식별자, 단위, 창 bracket, 결손만 본다.
- GC는 새 의미로 보고한다.
- E5-7 recovery 입력 처리도 포함한다.

구현 1·2·3은 서로 독립이라 병렬로 할 수 있다. 파일 영역은 httpapi / node-agent + `resources` 기록 / observer로 나눈다. 차트·배포와 하네스 전환은 셋 뒤에 한다. settings 쓰기 클라이언트 두 곳의 수정은 구현 1과 같은 변경 단위다.

## 5. 완료 기준

- E5 행 폴더의 CHILL 사실이 전부 CHILL API 호출 결과에서 나온다.
- CHILL 구성요소(서빙 인스턴스, 제어 Pod, node-agent, CHILL ConfigMap)를 대상으로 한 `sudo`, `crictl`, `/proc`, `aws s3`, SSH 수집, `kubectl` 읽기가 하네스에 남지 않는다.
- CHILL 밖으로 남는 것은 다음뿐이다.
  - edge-metrics 전력
  - 부하 생성기 기록과 `client` 자원 표본, 생성기 Job 조작(`get pod`, `logs`, `exec`)
  - 운영자 E5-7 명령 기록
  - 실행 중 안전 감시(STOP)
- 판정 수식은 유지하고 입력 파서만 바꾼다. 의미가 바뀌는 항목은 명시한다.
  - GC: 사이클별 CPU 분포 → 창 안 누적 차와 사이클 수. 이전 분포와 같다고 하지 않는다.
  - E5-7 recovery 참조의 출처: 운영자 readback → `scheduler`·`cluster` 행.

## 6. 하지 않는 것과 열린 점

- 가공 조회 API를 원시 내보내기로 대체하지 않는다. 두 용도가 다르다.
- 해시·바이트 비교를 상시 검증 관문으로 만들지 않는다(AGENTS.md). 전환 확인은 4장의 1회 비교뿐이다.
- 대시보드는 `/observations/raw`를 쓰지 않는다. 앞단(BFF)에도 열지 않는다. 대시보드의 자원·장애 진단 화면(레벨 2의 Node 자원, Pod 사건)은 `resources`·`cluster`의 **가공 조회**가 필요하다. 이번 범위에는 넣지 않고, 화면이 실제로 필요해질 때 기존 `/observations/*` 가공 경로에 추가한다.
- 라이브 DTO 추가(`instances[].waiting`, `reservations[]`, Node `ready/measurement/reasons`)는 이 계획이 아니라 대시보드 계획 4장 선행 수정이 맡는다. 워크로드 유입 큐 길이(`workloads[].pending`, runtime `WorkloadState.Pending`)도 같은 곳에서 사용자 결정 대기다.
- 안전·품질 관문을 "하네스 순감소"를 이유로 지우지 않는다. 지우는 것은 중복 수집 경로뿐이다.
- 게이트웨이 분리(ADR 0029)는 보류다. 실제 split이 승인되면 캠페인 사이 별도 작업 단위에서 source 차원·`resources`·`cluster`·T5 모집단을 함께 바꾼다. 이번에는 `router` 파라미터를 만들지 않는다.
- 다음 서버 재빌드와 이 계획의 리허설은 `e947cd5e`의 공유 `Runtime` 변경(예: `internal/serving/runtime.go:1208` `Routable`의 `i.Capacity > 0`)을 정리한 소스로 한다. #213 서버(`24f4ff4f`)는 그 이전 소스다.
- 하네스 자신의 부하 생성 Job 수명(`job-*`, `producer-*`)은 실험 장비 기록이라 범위 밖이다.
- 열린 점: node-agent가 비root 상태로 호스트 cgroup 파일을 읽을 수 있는지, edge Node에서 서버 수신 경로까지 도달하는지 구현 2에서 한 번 확인한다.
- 열린 점: 작은 Node(rasp5, jetson-nano)에서 1초 표본의 오버헤드를 구현 2에서 한 번 측정해 기록한다. 너무 크면 설정값으로 주기를 낮춘다.
- 열린 점: 묶음 전송 지연과 수집 종료 조건은 설계에서 정한다. 서버가 멈춰 있던 동안의 사실은 결손으로 남긴다. 정상이나 무사건으로 바꾸지 않는다.

# E5 증거를 CHILL API로: 우회 수집 제거 계획 (2026-10-28)

상태: 계획(사용자 범위 확정 2026-10-28). 적용 시점은 진행 중인 E5(#213)와 결과 표 기입이 끝난 뒤다. 그때까지 `hack/e5`·`load-generator`는 동결한다.

## 1. 목표와 범위

다음 E5부터 하네스는 **CHILL 내부 사실을 CHILL API로만** 가져온다. `sudo`+`crictl`+`/proc/<pid>/root` 읽기, S3 직접 다운로드, Node별 SSH cgroup 수집기, `kubectl` 스냅샷·로그, 별도 감시 로그 복사는 모두 없앤다.

| 구분 | 출처 | 이 계획에서 |
|---|---|---|
| CHILL 내부 사실: 요청 수신·결과·실행, 스케줄러 결정·스냅샷·동작·예약·경로, 정의·카탈로그, 인스턴스와 CHILL 자신의 자원 사용, 서빙 Pod·Node 상태 변화, 장애 감지·격리·대체·복구 | CHILL | **API로 제공** |
| 실제 전력 | edge-metrics API | 범위 밖(이미 API 있음). 단 "어느 Node가 언제 POOL에 속했나"는 CHILL 사실이라 API로 |
| 클라이언트가 본 요청 결과·송신 시각 | 부하 생성기 | 범위 밖(CHILL 외부) |
| 장애 주입 명령(E5-7) | 운영자 kubectl | 범위 밖. CHILL은 **감지와 대응만** 기록(사용자 결정 ①) |

## 2. 현재 우회 수집과 대체

근거: 인벤토리 `agent://EvidenceInventory`(2026-10-28). 약어 E=`hack/e5/e5.py`, A=`hack/e5/e5-archive.py`.

| 현재 증거 | 지금 수집 방법 | 대체 |
|---|---|---|
| `obs/<hour>/{requests,scheduler,catalog,management}.jsonl.gz`, `obs/definitions.jsonl` | 현재 시간: sudo→crictl→`/proc/<pid>/root` 직접 읽기(A:33-103). 지난 시간: `aws s3 cp`(A:249-276) | **API-1 원시 관측 내보내기** |
| `<node>-cgroup.jsonl`(인스턴스·게이트웨이·edgemesh 컨테이너 CPU ns, throttle, 메모리), thermal 행 | SSH로 각 Node에서 sudo crictl→`/proc/<pid>/cgroup`→sysfs, 1초(`hack/e5/d2/sample_cgroups.py:38-94`) | **새 관측 종류 `resources`** → API-1 |
| `cgmon.log`(게이트웨이 CPU·throttle·메모리) | 별도 감시 로그 통째 복사(E:1313-1315) | `resources`(서버 자기 cgroup) |
| `server.log`의 GC CPU | `kubectl logs` + gctrace 파싱(E:1316-1319, 1624-1632) | `resources`에 Go `runtime/metrics` GC CPU(이미 `internal/serving/overload.go`가 같은 패키지 사용) |
| `pre/post-pvc-usage.json`(관측 디렉터리 바이트) | sudo+`/proc` 읽기(E:885-893) | `resources`에 관측 저장소 바이트 |
| `pre/post-pods.json`, `pre/post-nodes.json` | `kubectl get pods -A`, `get nodes`(E:873-884) | **새 관측 종류 `cluster`** → API-1 |
| `control-alerts.jsonl`(제어 Pod 재시작·교체) | 별도 감시 프로세스 로그 복사(E:981-992) | `cluster` |
| E5-7 대응(격리·대체·Ready·경로 회복) | 운영자 readback 파일 참조(E:190-215) | `scheduler` + `cluster` 기록. 운영자 파일은 **명령 사실만** 남김 |
| `capture-identity.json`, `raw-source-index.json`, `s3-hour-list-*`, `upload.log`, `collector-status.json`, `*-collector.stderr` | 위 우회 경로의 수집 영수증 | **삭제**. 출처가 API가 되면 필요 없음 |
| `pre/post-{state,pools,settings}.json`, 경계·barrier·static 상태 | 이미 CHILL API(E:845-874) | 유지 |

## 3. 추가할 것 (최소 단위)

새 엔드포인트는 1개, 새 관측 종류는 2개다. 기록은 이미 있는 ObservationLog(분 단위 세그먼트, 시간 단위 불변 S3 보관, `internal/serving/observation_log.go:795`)에 쓰고, 읽기는 이미 있는 과거 조회 경로(로컬 우선, 없으면 보관본: `observation_log_read.go`)를 재사용한다.

### API-1 원시 관측 내보내기

- `GET /api/v1/observations/raw?hour=YYYYMMDDTHH` → 그 시간에 있는 종류·분 세그먼트 목록, 시간 닫힘 여부.
- `GET /api/v1/observations/raw/{hour}/{kind}` → **닫힌 분 세그먼트를 원래 바이트 그대로** 이어서 스트리밍(시간이 닫혔으면 보관본 gzip 그대로). `kind`: `requests`, `scheduler`, `catalog`, `management`, `resources`, `cluster`, `definitions`(루트 파일, hour 무시).
- 열린(현재) 분은 내보내지 않는다. 하네스는 단계 drain 뒤 다음 분 경계까지 기다렸다 가져온다.
- 인증은 기존 TokenReview/SAR(`httpapi/handler.go:159-169`), SAR 경로 `/chill/api/v1/observations/raw`로 별도 권한을 둔다(요청 ID 포함 원시라서).
- 하네스의 판정 코드는 지금도 이 원시 행을 읽는다(`hack/e5/e5-raw.py`). 바뀌는 건 **가져오는 방법뿐**이다. 가공 API(`/observations/workloads`, `/scheduler`)는 대시보드용으로 그대로 둔다.
- 게이트웨이 분리(ADR 0029) 뒤에는 라우터별 스트림(`observation_router.go:41`의 `routers/<id>/`)도 같은 엔드포인트에 `router` 파라미터로 노출한다.

### 관측 종류 `resources`

- **서버(게이트웨이·제어)**: 자기 컨테이너 cgroup v2 파일에서 CPU ns, throttle 횟수·시간, 메모리. Go `runtime/metrics`의 GC CPU. 관측 디렉터리 사용 바이트.
- **각 Node**: 이미 Node마다 도는 `cmd/node-agent`가 CHILL이 배치한 서빙 인스턴스 컨테이너(+edgemesh)의 같은 cgroup 값과 thermal zone 온도를 샘플링해 서버로 보낸다. 서버가 `resources`로 기록한다.
- 주기는 설정값(기본 cgroup 1초, thermal 5초. 현재 수집기와 같음). Pod·컨테이너 UID와 재시작 횟수를 함께 기록한다.

### 관측 종류 `cluster`

- 서버 observer가 이미 watch하는 Pod·Node 변화 중 **CHILL 서빙 인스턴스, CHILL 제어 Pod, POOL Node**에 한해 사건을 기록한다: Pod 생성·삭제(UID, Node, 이미지, 자원), phase·Ready·재시작, Node Ready·`unschedulable`(cordon).
- 이것으로 `pre/post-pods/nodes`, `control-alerts`, E5-7의 "CHILL이 언제 무엇을 봤는가"를 대신한다.

## 4. 순서 (spec → SRS → 설계 → 구현)

| 단계 | 할 일 | 산출 |
|---|---|---|
| spec | `docs/spec/00_measurement.md` "Serving 관측" 행(18행)에 "CHILL은 서빙 관측, CHILL 구성요소와 인스턴스의 자원 사용, 서빙 Pod·Node 상태 변화를 기록하고 원래 기록 그대로 API로 제공한다" 추가 | 한 커밋 |
| SRS | scheduler SRS에 요구사항 3개(원시 내보내기, `resources`, `cluster`)와 수용 시나리오 | SCH-67~ / SCH-AC-52~ |
| 설계 | `docs/design/serving/observability-split.md`에 두 종류의 행 형식·주기·크기, 내보내기 의미(닫힌 분만, 바이트 보존). ADR 0030: 저장 계약 변경(ADR 0019에 따라 한 번에, 캠페인 사이에) | 한 커밋 |
| 구현 1 | API-1(기존 세그먼트 경로 재사용) | 서버 |
| 구현 2 | `resources`: 서버 자기 측정 + node-agent 샘플링·전송 | 서버, node-agent |
| 구현 3 | `cluster`: observer 사건 기록 | 서버 |
| 하네스 전환 | `e5-archive.py`의 host·S3 경로, `sample_cgroups.py` 실행, cgmon·control-alerts 복사, kubectl 스냅샷·로그, pvc 읽기를 API-1 호출로 교체. 수집 영수증 파일 삭제 | `hack/e5` 순감소 |
| 확인 | 짧은 E5 리허설(E5-0 + 혼합 60초)을 새 경로로만 실행, 판정 결과(KPI 값)가 옛 경로와 같은 내용인지 **한 번** 확인 후 옛 경로 삭제 | 비교 1회, 상시 검증 아님 |

구현 1·2·3은 서로 독립이라 병렬로 할 수 있다(파일 영역: httpapi / node-agent+`resources` 기록 / observer). 하네스 전환은 셋 뒤에 한다.

## 5. 완료 기준

- E5 행 폴더의 CHILL 사실이 전부 CHILL API 호출 결과에서 나온다. 하네스에 `sudo`, `crictl`, `/proc`, `aws s3`, SSH 수집, `kubectl get pods/nodes/logs`가 남지 않는다.
- 외부로 남는 것은 edge-metrics(전력), 부하 생성기 기록, 운영자 E5-7 명령 기록 세 가지뿐이다.
- 기존 판정(`judge`, `summarize`, `tables.py`)은 입력 출처만 바뀌고 규칙은 그대로다.

## 6. 하지 않는 것과 열린 점

- 가공 조회 API를 원시 내보내기로 대체하지 않는다. 두 용도가 다르다.
- 해시·바이트 비교를 상시 검증 관문으로 만들지 않는다(AGENTS.md). 전환 확인은 4장의 1회 비교뿐이다.
- 하네스 자신의 부하 생성 Job 수명(`job-*`, `producer-*`)은 실험 장비 기록이라 범위 밖이다.
- 열린 점: 작은 Node(rasp5, jetson-nano)에서 node-agent 1초 샘플링의 오버헤드는 구현 2에서 한 번 측정해 기록한다. 너무 크면 주기를 설정값으로 낮춘다.
- 열린 점: 한 시간 요청 원시가 수백 MB다(E5-1 기준 tar 약 243 MB). API-1은 메모리에 올리지 않고 스트리밍해야 한다.

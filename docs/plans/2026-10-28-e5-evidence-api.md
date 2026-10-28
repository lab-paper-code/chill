# E5 증거를 CHILL API로 (2026-10-28)

적용은 E5(#213) 결과 표 기입 뒤다. 그때까지 `hack/e5`와 `load-generator`는 동결한다. 예외는 0장 publication 계약이다.

## 0. publication 계약 (#213 게시 전, 가장 먼저)

publication(`hack/e5/e5.py:1721-1787`)은 결과 표와 대시보드 S1이 함께 읽는 불변 기록이다. 형식은 이 계획이 맡는다.

- `kpis`에 `energyCoverage`, `gapErrorBoundJoules`를 추가한다. 공백 보간 규칙(`198150d6`)이 만든 값이다.
- 실행마다 측정 규칙 식별자를 넣는다. 항목은 하네스 SHA, `energyCoverage` 정책, 증거 출처(`host-s3` / `api`), GC 지표 의미(`gctrace-cycles` / `runtime-metrics-delta`)다.

## 1. 범위

하네스는 CHILL 내부 사실을 **CHILL API로만** 가져온다. 다음 우회 수집은 없앤다: `sudo`·`crictl`·`/proc` 읽기, S3 직접 다운로드, SSH cgroup 수집기, CHILL 대상 `kubectl` 읽기, 감시 로그 복사.

다음은 범위 밖이며 지금 그대로 유지한다.

| 항목 | 이유 |
|---|---|
| 실제 전력 | edge-metrics API가 이미 있다. 단 전력계↔Node 매핑과 POOL Node 목록은 CHILL 사실이므로 이 계획에 넣는다 |
| 부하 생성기 기록, `client` 컨테이너 자원, 생산자 해제 절차 | CHILL 외부 장비 |
| 실행 중 안전 감시(`control-alerts` STOP 루프) | 운영 안전 장비. 사후 증거만 `cluster`로 옮긴다 |
| E5-7 장애 주입 명령 | 운영자 kubectl. CHILL은 감지와 대응만 기록한다 |

## 2. 대체 표

약어: E=`e5.py`, A=`e5-archive.py`, W=`e5-wire.py`.

| 현재 증거 | 대체 |
|---|---|
| `obs/<hour>/*.jsonl.gz`, `definitions.jsonl` (A: host·S3) | 원시 내보내기 API |
| `raw-source-index.json` | **유지.** 판정 입력이다. API 수집기가 생성한다 |
| `<node>-cgroup.jsonl`의 server·instance 행, thermal | `resources` |
| `<node>-cgroup.jsonl`의 `client` 행 | **유지.** 시작 관문과 생산자 해제가 쓴다 |
| `cgmon.log` | `resources` |
| `server.log`의 GC | 서버 `runtime/metrics` 누적값. 의미가 바뀐다(5장). W의 gctrace 요구는 삭제 |
| `server.log`의 observer 오류 | `cluster` issue 행 |
| `pre/post-pvc-usage.json` | 서버의 관측 디렉터리 바이트(분 1회) |
| `pre/post-pods.json`, 제어 이미지 digest | `cluster` Pod 행 |
| `pre/post-nodes.json`, 전력 reducer `--nodes` | 기존 state의 Node 참조. NodeList를 합성하지 않는다 |
| E5-7 운영자 readback | `scheduler`·`cluster` 행. 운영자 파일에는 명령만 남긴다 |
| `metric-sources.yaml` (ConfigMap) | settings의 `meters` |
| 전력 관측 정책 (ConfigMap) | 이미 있는 settings의 `powerObservation` |
| 수집 영수증(`capture-identity`, `s3-hour-list-*`, `upload.log`, `collector-status`, `*-collector.stderr`) | **삭제.** 이 파일을 읽는 판정 코드도 함께 정리한다 |

## 3. 추가할 것

기록은 기존 ObservationLog에 한다. 읽기는 기존 로컬 우선→보관본 경로를 재사용한다.

### 원시 내보내기

- `GET /api/v1/observations/raw?hour=` → 분 세그먼트 목록과 시간 상태(`open` / `closed-local` / `archived`).
- `GET /api/v1/observations/raw/{hour}/{kind}`
  - 로컬 닫힌 분은 JSONL 원본을, 보존 2시간이 지난 시간은 보관본 gzip을 그대로 보낸다. 서버가 즉석에서 압축하지 않는다.
  - `kind`: `requests`, `scheduler`, `catalog`, `management`, `resources`, `cluster`.
- `GET /api/v1/observations/raw/definitions` → 확정된 마지막 완결 행까지만 보낸다.
- 열린 분은 내보내지 않는다. 하네스는 drain 뒤 다음 분 경계가 지나면 가져온다.
- 처리 마감과 쓰기 마감을 따로 둔다(`longOperation` 방식). 이 보관본 읽기·마감 정책은 대시보드가 쓰는 가공 조회(`/observations/scheduler`)에도 적용한다.
- 권한: nonResourceURLs에 `/raw`와 `/raw/*`를 둘 다 넣는다. 하네스 전용이며, 대시보드와 앞단에는 열지 않는다.

### `resources`

- **node-agent**(모든 Node, genesis1 포함)가 CHILL 서빙 인스턴스와 CHILL 제어 Pod 컨테이너를 잰다.
  - 값: cgroup counter(CPU, throttle, 메모리), 센서별 thermal 온도.
  - 행 필드: `node`, `podUID`, `containerID`, `cgroupVersion`, 표본 시각, 서버 수신 시각.
- **컨테이너 발견**: 호스트 `/proc`·`/sys`에서 Pod UID와 containerID로 찾는다. 환경마다 cgroup v1/v2와 경로 형식이 섞여 있으므로 경로 모양을 추측하지 않는다.
- **전송**: 내부 수신 POST 1개, SA 토큰 + 기존 TokenReview/SAR, 묶음 전송(Node당 1행).
  - 재전송 장치는 없다. 전송 실패 구간은 결손으로 남긴다.
  - 하네스는 측정 창 끝 + 묶음 주기 + 다음 분 경계가 지난 뒤 수집한다.
- **서버 in-process**: GC 누적 CPU와 사이클 수(기존 1초 ticker), 관측 디렉터리 바이트(분 1회). 서버가 자기 cgroup을 직접 읽지는 않는다.

### `cluster`

- **Pod**: CHILL 인스턴스와 제어 Pod의 생성·삭제·Ready·restart·종료 사유·imageID를 기록한다. 서버는 기동 때 자기 Pod UID를 1행 기록한다.
- **observer issue**: 발생·변경·해소를 기록한다.
- **Node**: 따로 기록하지 않는다. 이미 scheduler 행에 있다. cordon은 기록하지 않는다.
- 기록이 성공하면 같은 사실을 라이브 hub로 넘긴다(판단·작업과 같은 callback 방식). event의 형태는 대시보드 계획이 정한다.

### settings `meters`

- 서버가 수락한 전력계 선언을 settings GET에 **읽기 전용**으로 노출한다.
- 같은 변경에서 settings를 GET해서 고친 뒤 PUT하는 클라이언트 두 곳이 `meters`를 빼고 보내도록 고친다. 고치지 않으면 서버가 모르는 필드로 거부한다.
  - 하네스 `CPO.put`
  - 대시보드 `putSchedulerSettings`

## 4. 순서

| 단계 | 할 일 |
|---|---|
| spec | `00_measurement.md` "Serving 관측" 행에 "자원 사용·Pod 변화·장애 감지를 기록하고 원래 기록 그대로 API로 제공한다"를 추가 |
| SRS | 요구사항 4개(원시 내보내기, `resources`, `cluster`, `meters`)와 수용 시나리오 |
| 설계 | `observability-split.md`. ADR 0030: 같은 prefix에 stream 2개 추가, 버전 유지. 배포 이전 시간에 stream이 없는 것은 결손이 아니다 |
| 구현 1 | 원시 내보내기, `meters`와 두 쓰기 클라이언트. stream 2개 등록 |
| 구현 2 | `resources` |
| 구현 3 | `cluster`와 라이브 callback |
| 배포 | RBAC(`/raw`, node-agent 수신)와 대시보드 계획 4장 항목을 **한 번의 승인 배포**로 묶는다 |
| 하네스 전환 | 2장 표대로 교체하고 영수증 파일을 삭제한다. `client` 수집과 STOP 루프는 남긴다 |
| 확인 | 아래 리허설 1회 |

구현 1·2·3은 병렬로 해도 된다.

**확인 리허설 (1회)**
- 옛 수집과 새 수집을 같은 리허설에서 함께 돌린다. #213의 보관된 1시간도 API로 받아 판정해 본다.
- 관측 KPI(D1, 지연, 전력 J)는 같아야 한다.
- `resources`는 식별자·단위·결손만 확인한다.
- 측정 대상 ChillServer에 더해지는 CPU 몫을 기록한다. 대상은 라이브 hub, node-agent 수신, 원시 내보내기, 대시보드 조회다.
- 확인이 끝나면 옛 경로를 삭제한다.

## 5. 완료 기준

- E5 행 폴더의 CHILL 사실이 전부 CHILL API에서 나온다. 하네스에 CHILL 대상 우회 호출이 없다.
- CHILL 밖으로 남는 것은 다음뿐이다: edge-metrics 전력, 부하 생성기 관련(`client` 표본, Job 조작), 운영자 E5-7 명령, STOP 감시.
- 판정 수식은 그대로다. 의미가 바뀐 항목은 보고서에 적는다.
  - GC: 사이클 분포 → 창 안 누적 차
  - E5-7 recovery 출처: 운영자 readback → `scheduler`·`cluster`

## 6. 규칙과 열린 점

- E5 측정 창 동안에는 원시 내보내기와 대시보드 과거 조회를 실행하지 않는다.
- 다음 서버 재빌드는 `e947cd5e` 분리 코드를 정리한 소스로 한다. 게이트웨이 분리는 보류(ADR 0029)이며 `router` 파라미터는 만들지 않는다.
- 열린 점(구현 2에서 1회 확인):
  - node-agent가 비root로 cgroup 파일을 읽을 수 있는지
  - edge Node에서 서버까지 닿는지
  - 작은 Node의 1초 표본 오버헤드

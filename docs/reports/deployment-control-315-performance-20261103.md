# control-315 배포 병목과 최소 개선

## 범위와 판정

2026-11-03, `hack/fast-deploy.sh` 단일 runner의 첫 실제 실행을 분석했다. 실행자는 main이며, source는 `1b81e0d5`, runner는 `f1fad9a0`, Helm revision은 178→179다.

**주 병목은 FirstFloor 이미지 빌드다. 별도로, 실제 API 준비 전에 Ready가 된 서버와 transport 오류를 즉시 종료한 accept 때문에 runner는 STOPPED였다.** main의 후속 수동 수용 확인과 runner 자동 성공을 구분한다. 아래 총시간은 STOP까지이며, 실제 API 준비·수용 완료까지의 시간이 아니다.

원시 증거는 저장소에 복사하지 않고 다음 private 경로에 보존한다.

- 기준 실행 `O = /home/genesis1/.cache/chill-assets/ops/pools-deploy/deployB/control-315-runner`
- 이번 검증 `V = /home/genesis1/.cache/chill-assets/ops/pools-deploy/intent/deploy-tool-review-20261103`
- `O/performance.json`, `O/command-timings.jsonl`, `O/*-build.log.stderr`, `O/main-manual-acceptance.md`
- 서버 초기화의 별도 조사: `intent/ux-survey-20261030/R18-coldstart.md`(위 pools-deploy 아래)

## 1. 실제 단계 시간

| 측정 대상 | 초 | 해석 |
|---|---:|---|
| runner 시작→STOP | 2,482.764 | 정상 완료 시간 아님 |
| publish-images | 2,106.105 | STOP까지의 84.8%; 10개 이미지 순차 빌드·발행 |
| stage | 322.472 | 승인 exact-image probe와 declaration 준비 |
| stage 안 runtime inspection 명령 | 313.265 | stage와 중첩; 둘을 더하지 않음 |
| readiness | 2.561 | listener 준비를 보장하지 못한 당시 값 |
| accept | 1.249 | 첫 coverage transport 오류로 종료 |

다음 표는 Buildx 명령별 벽시계 시간이다. vertex는 로그에서 확인한 주요 비용이며, 병렬 vertex·반복 출력된 누적 DONE·export 하위 push를 중복 합산하지 않았다.

| 이미지 | Buildx 초 | 주요 로그 근거(초) |
|---|---:|---|
| serving-entrypoint | 39.491 | Go 33.1, export 3.0 |
| profiler | 57.032 | Go 31.7 + 20.0, export 3.0 |
| query | 36.955 | Go 31.8, export 3.1 |
| dashboard | 27.792 | install 4.6, build 16.7, export 2.2 |
| FirstFloor CPU | 315.542 | arm64 pip 294.3; amd64 pip 25.9; Go 65.0/65.1은 병렬 |
| FirstFloor CUDA | 154.881 | base 71.8, apt 19.2, pip 34.5, export 25.0 |
| FirstFloor L4T r32 | 168.502 | base 64.4, vendor 준비 66.6, pip 27.7 |
| FirstFloor L4T r35 | 436.020 | base 212.1, vendor 준비 26.9, pip 174.1 |
| FirstFloor L4T r36 | 566.508 | base 207.7, apt 60.7, pip 278.1 |
| FirstFloor RKNN | 302.153 | pip 275.2, helper 14.6, export 9.4 |

6개 FirstFloor 명령은 전체 이미지 Buildx 시간의 92.3%다. vendor context 전송은 51.67kB/0.1초로 이 실행의 주 병목이 아니다. 따라서 context 축소나 control Go 빌드만 고치는 것보다 runtime base·의존성 설치의 반복 비용을 먼저 줄인다.

빌더 cache snapshot은 20.68GB/248 records→20.4GB/143 records였으며, ID 기준 136개가 사라지고 31개가 생겼다. cache 압력과 일관되지만 특정 pip layer가 GC로 사라졌다는 증거는 아니다. daemon GC 한도 변경·전체 빌드 병렬화·cache registry 신설은 하지 않았다. 디스크 사용률 85% 제약 아래에서 근거 없는 동시 실행은 피한다.

## 2. 적용한 빌드 개선

`build/docker/firstfloor.Dockerfile`의 final stage에서 실행 조건과 의존성 경계를 맞췄다.

1. Python 관련 ARG를 apt 뒤에 둔다. Python 값만 달라져도 OS 설치 cache가 무효화되던 연결을 없앤다.
2. 공통 NumPy·ORT·module 설치 뒤에 `EXTRA_PACKAGES`를 선언하고 extras를 별도 RUN으로 설치한다. base·공통 인자가 같은 CPU arm64와 RKNN이 공통 설치 layer를 재사용할 수 있다.
3. extras 설치에도 공통 명시 요구사항을 함께 전달한다. 정상 pip resolver를 사용하며 직접 pin 충돌을 무시하거나 `--no-deps`로 덮지 않는다.

6종 base, 명시 버전 pin, 아키텍처, vendor 준비, binary copy, 사용자, entrypoint는 변경하지 않았다. extras가 없는 이미지에는 조건부 no-op RUN만 추가된다. cache가 유지될 때의 재사용 개선이며, L4T base pull이나 처음 수행하는 arm64 설치 자체를 없애지는 않는다. 두 번의 pip 해석이 모든 전이 의존성의 byte-for-byte 동일한 설치 집합을 보장한다고 주장하지 않는다.

### 실제 로컬 검증

기존 `chill-builder`와 `/tmp/chill-heavy.lock`을 사용했다. Dockerfile의 실제 의존성 stage만 실행했고 Go/vendor stage·모델 실행·GPU·클러스터·image push/load는 실행하지 않았다.

- native amd64 공통 설치 후 extras만 변경: apt와 공통 pip layer가 실제 CACHED였다.
- module 요구사항 변경: apt는 CACHED, 공통 pip 설치는 다시 실행됐다.
- 실제 설치 결과 `pip check`와 NumPy 2.2.6 / ORT 1.27.0 / Jinja2 3.1.6 확인이 통과했다.
- 충돌 extras `numpy==1.26.4`는 고정 요구 `numpy==2.2.6`과 함께 `ResolutionImpossible`로 실패했다. 버전 완화나 강제 덮어쓰기를 하지 않음을 확인했다.

증거: `V/control-315-dependency-cache-smoke.log`, `V/dependency-conflicting-extra-smoke.log`.

이는 cache 경계와 native 의존성 동작의 증거다. **6종 전체 재빌드·arm64/vendor 실행 호환성·실제 배포 단축 시간은 아직 측정하지 않았다.** 다음 main 실행의 같은 Buildx 역할별 시간으로 대조해야 한다. 조건이 다른 native smoke 시간을 control-315의 arm64 설치 시간과 비교하지 않는다.

## 3. 서버 시작 지연과 잘못된 Ready를 분리

main의 관측에서 서버 Pod는 23:30:03Z Ready가 됐으나 첫 accept(23:30:09Z)는 transport 오류였고, TLS listener는 약 6–7분 뒤 열렸다. 당시 chart에 readinessProbe가 없었다. 별도 R18 조사에서 초기 catalog 검증은 329.015초/8,556 S3 GET을 사용했다. 동기 catalog refresh와 serving 초기화가 끝나야 listener가 열린다는 서버 소스와 일치한다.

- chart: 기존 named port `https`에 **HTTPS `/healthz` readinessProbe**를 추가한다. 새 endpoint나 liveness restart는 만들지 않는다. `/readyz`는 observation health로 흔들릴 수 있어 사용하지 않는다.
- Helm: probe가 초기화를 실제로 기다리므로 기존 고정 8분 대신 recipe `upgradeSeconds`(기본 900초)를 `--wait --timeout`에 사용한다. 프로세스 제한은 선택값+30초이며 양쪽 토큰 사전 예산도 같은 값을 반영한다. 큰 catalog에는 운영자가 더 긴 값을 지정할 수 있다. timeout 후 자동 재시도·failed release 숨기기는 하지 않는다.
- runner: rollout·Pod 검사와 accept가 하나의 readiness deadline을 공유한다. connection refused/reset/timeout, TLS handshake EOF, HTTP 503 `CoverageWarming`은 남은 시간 안에서 GET 대기한다. coverage가 warming이면 profiles 다운로드를 반복하지 않는다.
- TLS 인증서·프로토콜 오류, 401/403, 다른 503, 잘못된 JSON/source는 즉시 실패한다. 전체 오류를 삼키거나 배포 자체를 재시도하지 않는다.
- 시도별 HTTP/body와 transport 종류·errno·대기 가능 여부를 보존한다. kubelet probe의 인증서 비검증과 runner의 CA/hostname 검증을 혼동하지 않는다.

실제 loopback HTTPS regression은 listener 부재→listener 시작→CoverageWarming→성공, TLS handshake 도중 EOF→성공, profiles 연결 종료→성공, deadline 종료를 실행한다. 잘못된 인증서·plaintext protocol·권한·응답은 대기하지 않고 실패한다. 새 EOF case는 수정 전 실제 `ssl.SSLEOFError`에서 실패했고 수정 후 통과했다. fake-tool 실제 runner CLI smoke도 warming 후 완료 경로를 검증한다.

명시 release `chill`, namespace `chill-system`, control-315의 캡처 `live-values.json`으로 profiler false/true를 각각 렌더링해 실제 serving Deployment의 HTTPS probe와 named port 8080을 확인했다(`V/readiness-probe-serving-{default,profiler}.yaml`).

이 변경은 준비 판정의 정확성을 고친다. catalog 계산 자체를 빠르게 하는 변경은 아니다. backend 소유 advisor-2의 `689863b6`은 CompletedTrials 재검증을 최대 8개 병렬화한다. 이것의 새 live 시작 시간 역시 아직 실측 전이다.

## 4. 다음 실제 배포에서 비교할 것

main만 새 source와 revision을 확정해 실행한다. control-315 선언은 당시 source/revision 기록으로 남기며 재실행하지 않는다.

- 동일 역할별 Buildx wall time, base/pip/export vertex, builder cache 전후를 비교한다. cache·source·registry 상태 차이를 함께 기록한다.
- Pod 생성/Ready, listener 첫 응답, coverage 첫 200, 최종 accept 완료를 구분한다. STOP 시간이나 mtime을 성공 시간으로 바꾸지 않는다.
- probe가 실제 초기화를 기다리는지와 기존 profile·측정값·coverage 수치를 별도로 확인한다. usable 수는 표시이며 그 값만으로 배포를 막지 않는다.
- control-315의 수동 수용은 main의 증거다. 이번 수정은 로컬 검증이며 아직 새 chart/runner의 live 배포 증거가 아니다.

# CHILL 캠페인 과거 기록 — ARM64 실행 이미지 검증

[현재 목표·판정·다음 의존성](2026-10-13-scheduler-live-evidence-campaign.md)

2026-10-13의 런타임 smoke 기록이다. 당시 미검증 범위·의존성은 현재 작업 지시가 아니다.


### 2026-10-13 — rasp5용 firstfloor ORT CPU 이미지 확보·실행 검증

**결과: 선택한 런타임 검증 단위 완료. 정식 ProfilingRun/campaign은 미실행.**

- 기존 `build/docker/firstfloor.Dockerfile`로 `edge-builder`에서 `linux/arm64` 이미지를
  빌드하고 registry에 게시했다. production source·Dockerfile은 변경하지 않았다.
- 게시 태그: `155.230.35.213:5000/chill/chill-firstfloor:rasp5-ortcpu-20261013-unit1`.
- 검증 Pod가 소비한 불변 이미지:
  `155.230.35.213:5000/chill/chill-firstfloor@sha256:bd7435f81d36ea1ca5c692293023e2ed1fffcc67490f5592ffa64e2d2a448d93`.
- 위 OCI index의 ARM64 manifest:
  `sha256:7b919a87721d2d0ff30da488a8b9ee215252c63b49facda1faaae32784b6da9a`.
- 기존 모델은 `mobilenet-v2-050`, artifact image는
  `155.230.35.213:5000/chill/chill-mobilenet-v2-050-artifact@sha256:1174f5266a0a4f8b968f2a8745671cb2546934373766797b256fe97d38088721`.
- 실제 Node UID: `1d8b2d96-a51a-40c1-b020-a4a63daf7118`.
- 검증 Pod: `chill-system/chill-firstfloor-rasp5-unit1`,
  UID `dbe977b1-7e18-4ba2-b394-3ca43fbc80e8`, `Succeeded`, runtime container exit 0,
  실행 구간 `2026-10-13T08:36:50Z`~`08:36:53Z`. worker 종료도 exit 0이었다.
- 실제 `aarch64` / ORT `1.27.0` / NumPy `2.2.6` /
  `CPUExecutionProvider` / `synthetic=false`를 metadata로 확인했다.
- CPU request/limit 1, memory request/limit 512Mi, Pod QoS Guaranteed.
  관측 quota `100000 100000`, cgroup cpuset `0-3`, 프로세스 affinity `[3]`,
  intra/inter-op threads 각각 1. affinity 지정은 전용 CPU 할당의 증거가 아니다.
- worker의 `profiling` mode에서 persistent runner를 유지하고 `/profile`로
  고정 K=1·2를 각각 3회 실행했다. batch별 runner warmup 2회는 표본에 포함하지 않았다.
- 입력은 NumPy default_rng seed 0의 FP32 standard-normal tensor이며 K=1 다음 K=2
  순서로 생성했다. 동일 입력의 직접 ORT CPU 출력과 `rtol=1e-5`, `atol=1e-6`로 비교했고
  모든 출력이 finite이며 실제 최대 절대 차이는 0이었다. 의미 정확도 평가는 아니다.

| 고정 K | 실행 수 | runner service 원시 표본 (ms) | 출력 shape |
|---|---|---|---|
| 1 | 3 | 22.546562, 19.293616, 19.348894 | `[1, 1000]` |
| 2 | 3 | 42.260754, 40.059777, 40.340742 | `[2, 1000]` |

- accepted/completed/executions는 각각 6, 남은 요청 0, 실제 batch ID는 1~6이었다.
  client/native/backend/service span과 전체 응답은 raw 결과에 보존했다.
- ORT가 GPU DRM device discovery 경고를 출력했으나 억제하지 않았다.
  실제 선택 provider는 CPU만이며 모든 출력·완료 검증이 통과했다.

#### 실행 및 보존 근거

- 첫 `--push`는 HTTP registry를 HTTPS로 접속하여 실패했다. `edge-builder`의 해당 게시에만 `--output type=image,...,push=true,registry.insecure=true`를 지정해 성공했으며 외부 Docker/BuildKit 설정은 변경하지 않았다.
- 격리 ConfigMap·Pod를 생성하여 init carrier에서 모델을 복사하고, loopback worker와
  실제 persistent ORT runner를 실행했다. 서비스 노출·서비스 계정 token mount는 없었다.
- 초기 `kubectl logs -f`는 생성 직후 Pod not found로 종료됐다. Kubernetes API의 실제
  Succeeded 상태와 이후 logs 조회로 출력·실행 증거를 확보했다.
- 로컬 raw 보존 파일:
  `/tmp/chill-rasp5-firstfloor-unit1-build.json`,
  `/tmp/chill-rasp5-firstfloor-unit1-result.json`,
  `/tmp/chill-rasp5-firstfloor-unit1-pod-evidence.json`,
  `/tmp/chill-rasp5-firstfloor-unit1.log`.
  이는 로컬 임시 경로에 보존한 smoke 기록이며 Evidence Store의 정식 evidence가 아니다.
- 검증용 Pod·ConfigMap을 삭제했고 재조회에서 남은 객체가 없음을 확인했다.
  throwaway 실행 script·Pod manifest도 정리했다. 게시 이미지는 후속 측정을 위해 유지한다.

#### 당시 미검증 범위

- 전력·idle·에너지·memory peak·관측 coverage, production `measurement.py` 전체 실행,
  ProfilingRun controller·정식 evidence 보존·scheduler admission은 이번 단위에서
  검증하지 않았다. 이 표본을 scheduler가 소비할 qualifying profile로 취급하지 않는다.
- 기존 Profiler 설정·ModelSpec·Workload·전력모드·scheduler 제어는 변경하지 않았다.
  다른 세션의 Dashboard 변경을 보존했다.
- 소스 변경이 없어 `make lint test`, architecture/gopls, Helm 및 e2e gate는 실행하지 않았다.
  이미지 빌드와 실장비 추론 smoke가 이번 단위의 실행 검증이다.
- 당시 firstfloor Profiler 실행 선언·정식 frozen 측정·observer 연결은 미실행이었다. 후속 결과는 [실행 선언 기록](2026-10-13-scheduler-live-evidence-campaign-02-declarations.md)에 있다.


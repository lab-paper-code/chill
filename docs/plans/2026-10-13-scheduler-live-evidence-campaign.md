# CHILL 실클러스터 evidence 확보 및 FULL 제어 기능 검증

## 궁극적 목표

현재 활성화된 노드를 최대한 활용하여 CHILL 스케줄러가 실제 판단·제어에
소비할 수 있는 실측 evidence를 확보한다. 그 근거를 실제 스케줄러에 연결하고,
실클러스터에서 판단 → 제어 실행 → 실제 상태 변화 → 요청 처리 및 지연·전력
관측까지 종단으로 검증한다.

모델 신규 개발·학습은 이번 세션의 범위가 아니다. 기존 실행 산출물로 검증한다.
프로파일 생성이나 제어 플래그 활성화만으로 목표를 달성했다고 보지 않는다.
기능 검증 결과와 연구적 SLO 개선·에너지 절감의 입증은 구분한다.

기준 계약:
- [시스템 명세](../spec/README.md)
- [스케줄러 SRS](../spec/scheduler/SRS.md)
- [스케줄러 구현 및 검증 경계](../design/serving/scheduler-implementation.md)

## 완료 기준

- 현재 활성 노드를 모두 조사 대상으로 삼고, 노드별 지원 실행 경로·자원·meter
  대응을 확인한다. 실제 사용하지 못한 노드·경로는 이유와 미충족 의존성을 남긴다.
- 서빙과 동일한 firstfloor/backend 조건에서 실행시간 원시 분포, 배치별 증분
  에너지, 메모리 및 실행 조건을 확보하고, 필요한 idle·원자 동작·전력모드
  전환 비용 evidence를 보존한다. 임의 값이나 다른 실행 조건으로 대체하지 않는다.
- evidence가 실제 후보 평가에 채택되는 것을 확인하고 판단의 입력·후보·선택
  이유·예상 비용을 추적할 수 있다.
- placement, resources, replicas, sharing, routing, batching, power, membership을
  각각 검증한 뒤 함께 활성화한 FULL 제어를 검증한다. 지원되지 않거나 미검증인
  제어를 숨기고 FULL 통과라고 보고하지 않는다.
- 목표 선언과 실제 적용을 구분하고, readiness·분배 반영·drain·제거·membership·
  전력 read-back 등 해당 동작의 실제 완료를 확인한다.
- 일정 부하, 증가·감소, 버스트, 전환 직후 부하 반전, 용량 초과 및 테스트 전용
  대상의 실패 상황에서 판단·동작·요청 결과·종단 지연·전체 POOL 전력을 연결한다.
- 동일 부하의 제어 OFF 비교 기록을 확보한다. SLO 위반·실패·거절·취소·미완료와
  예측/실측 차이를 숨기지 않고 결과로 남긴다.
- 실제 `chill-system`의 CHILL Dashboard를 브라우저로 조작하여 인증,
  측정/evidence 조회, 스케줄러 판단·실제 전환, 추론·관측의 e2e 흐름을 검증한다.
  화면 표시를 실제 API 및 클러스터 상태와 대조하고 시각 증거를 남긴다.
  최종 화면에는 FULL 제어의 실측 근거 → 판단 → 실제 적용/전환 완료 →
  현재 서빙 상태·지연·전력이 명세의 의미대로 연결되어 보여야 한다.
  모의 데이터, 단순 FULL 활성화 표시나 페이지 로딩만으로 통과시키지 않는다.

## 반복 작업 규칙

사용자의 반복 지시:

> 해야하는 일의 의존성을 고려해서 다음 가장 먼저 해야하는 일 딱 1개만 Thinking And Step By Step.

1. 궁극적 목표와 직전 작업의 실제 결과를 기준으로 현재 미충족 의존성을 확인한다.
   자료로 알 수 있는 전제는 직접 확인하고, 조율이 필요하다는 일반론으로 대신하지 않는다.
2. 의존성이 충족되어 착수 가능한 작업 중 목표를 진전시키는 다음 작업 **딱 하나**를
   고른다. 전제가 미충족이면 그 전제를 해결하는 작업 자체를 고른다.
3. 선택 답변에는 현재 근거, 선행해야 하는 이유, 구체 작업, 관측 가능한 완료 기준만
   간결하게 적는다. 내부 사고 과정 대신 결정 근거를 설명한다.
4. 2026-10-13 사용자의 연속 실행 승인에 따라 작업 선택과 실행을 추가 동의 없이
   반복한다. 한 단위를 구현·실행·검증·기록한 뒤 다음 의존성을 선택하여 계속 진행한다.
   전체 목표 완료 전에는 단위 경계에서 멈추지 않는다.
5. 작업 완료 시 이 문서의 진행 기록에 실행 조건·증거 위치·결과·남은 의존성을 남긴다.
   실제 실행하지 않은 것을 통과로 기록하지 않는다. 실패는 원인을 기록하고 다음
   반복에서 해결할 의존성으로 다룬다.
6. 기본적으로 기존 API와 실행 owner를 사용한다. 명세 의도와 실제 동작의 불일치나
   필요한 기능 누락이 증거로 확인될 때만 코드를 수정한다.
7. 필요한 구현은 작은 커밋 단위로 수행한다. 구현 후 ponytail 리뷰로 과구현·과검증을
   확인하고, 필요한 리팩토링과 실제 실행·회귀 검증 후 해당 변경만 커밋한다.
   다른 세션의 변경은 커밋에 포함하지 않는다.
8. 각 중간 검증 전에 관련 `docs/spec/` 문서를 확인한다. 설계·현재 코드·테스트의
   통과 여부가 명세를 대신하지 않으며, 요구사항·실행 조건·실제 관측을 연결해 판정한다.
9. 데이터 모델은 최신 계약만 지원한다. 과거 버전 호환 reader·alias·fallback이나
   다중 버전 상태는 추가하지 않는다. 계약 변경 시 생산자·소비자·fixture·생성물·문서를
   함께 갱신하고, 이 세션의 불필요한 구 객체는 기록 후 정리한다.
10. 본 세션의 현재 사용자 입력을 수행 기준으로 삼는다. 오래된 요약·폐기된 계획·다른
    세션의 요청은 현재 지시로 재사용하지 않는다. 과거 raw evidence는 실험 증거일 뿐이다.
11. 나는 spec·의존성·통합·완료 판정을 소유하는 오케스트레이터다. 에이전트에는
    대상·행동·완료 증거가 명확한 최소 단위를 맡기고 독립 단위만 병렬 실행한다.
12. GPU 실행 프로파일을 우선한다. 예상 연산 전력 효율 순서는 가설로 구분하고
    동일 입력·실행 조건의 실측 에너지·처리량·지연으로 검증한다.
13. 중간 보고·의도 확인은 진행 상태를 설명하는 작업이지 최종 목표의 완료가 아니다.
    보고 뒤 같은 설명을 반복하지 않고 승인된 다음 핵심 작업을 실행한다. GPU 실행 환경
    구축·프로파일 수집도 FULL 제어와 실제 Dashboard 연결을 위한 의존성으로만 다룬다.
14. 토큰 집약적인 조사·구현·리뷰는 작은 계약으로 에이전트에 맡긴다. 부모 세션은
    세부 파일 전체를 반복해서 읽지 않고 인터페이스·위험·실행 증거만 확인한다.
    한 공급자의 실행 실패를 큰 작업 전체를 직접 떠안는 이유로 삼지 않는다.

## 실행 경계

- 작업 직전에 다른 세션의 변경과 현재 노드·자원·캠페인 상태를 확인한다.
  이전 스냅샷을 현재 상태로 간주하지 않는다.
- 다른 사용자 작업·서비스·객체를 임의로 중단하거나 삭제하지 않는다.
  실제 점유·부하 충돌이 확인되면 해당 노드 측정의 구체적인 선행조건으로 다룬다.
- 실클러스터 변경·이미지 게시·전력모드 변경은 사용자가 요청한 실행 범위 안에서만
  수행한다. 이 문서를 작성한 것이 그런 변경을 실행했다는 뜻은 아니다.
- 진행 중인 측정 캠페인의 stored evidence 계약을 변경하지 않는다.
- testbed kubeconfig로 저장소 e2e suite를 실행하지 않는다. 여기서 수행하는 기능
  검증은 명시적으로 범위를 정한 live 캠페인이며, kind 전용 e2e와 구분한다.
- Dashboard e2e는 위 사용자의 요청에 따른 실제 live 웹서비스 브라우저 검증이다.
  kind 전용 저장소 e2e suite를 testbed kubeconfig로 실행하는 방식으로 대체하지 않는다.

## 현재 상태와 미충족 의존성 — 2026-10-14

- CPU/firstfloor 조건의 5노드 q1/q2 10개 프로파일, 90 trial과 원자 제어의
  accepted reference 39개가 연결됐다. 이는 GPU 실행 근거나 FULL 통과가 아니다.
  genesis1/2, lattepanda, rasp5의 준비·유휴·제거와 POOL 탈퇴·편입을 검증했지만,
  실제 자원 변경의 종단 검증, OFF 비교, 8개 제어/FULL 및 SLO·절감 입증은 남아 있다.
- 첫 실제 추론은 완료됐지만 owner 종단 지연 `1.298374879s`는 frozen SLO `1s`를
  초과했다. **첫 표본 SLO 위반**이며 통과가 아니다. ingress validation/accepted
  elapsed `915.432437ms`, 실행 엔진 elapsed `130.358594ms`, 외부 client 왕복
  `1.3555289909709245s`는 서로 다른 범위다. 근거:
  `/tmp/chill-native-campaign-first-inference-owner-observations.json`.
- owner CPU 예산 수정은 실제 snapshot smoke에서 5개 노드 Ready를 확인했다.
  현재 release revision 23은 실행 인스턴스 Service 명칭 수정 owner와 날짜 입력 수정
  Dashboard를 배포했다. 날짜 입력은 실제 브라우저 DOM event/Apply로 유효한 변경,
  빈 값 거부와 fractional ISO 보존을 확인했다. 키보드 입력 자체는 미검증이며
  scheduler history 조회 504는 별도 미해결 의존성이다.
- Jetson 전력모드 측정은 다른 작업 소유자 조율까지 blocked다. 다른 작업을 중단하거나
  안전 정책을 넓혀 우회하지 않는다. orangepi memory peak 부재와 Orin Nano actuator
  readiness 문제도 성공으로 대체하지 않는다.
- 대형 journal의 전체 scan/shared lock은 알려진 연구 규모 한계다. 원본을 보존한
  별도 제어 시간창과 기본 1분 archive window는 그 한계의 bounded workaround이며,
  범용 성능 해결이나 일반적인 S3 네트워크 원인 확정이 아니다.
- **모델과 실행 엔진 이름의 혼동 정정은 pending**이다. firstfloor는 실행 엔진이지
  모델 정체성이 아니다. 원래 `mobilenet-v2-050`(UID `b8bc6efa..`)은 다른
  dashboard-validation Workload 2개가 참조하고, 이번에 등록한
  `mobilenet-v2-050-firstfloor`(UID `545ef2c0-b931-4c96-b978-b4cef1195210`)는
  `image-classification-tight-slo` / `image-classification-relaxed-slo`가 참조한다.
  과거 이름·measuredUID·측정 근거를 이미 이관한 것처럼 바꾸지 않는다.
- 명칭 정정은 `native-control-a/b`와 서비스 이름도 포함한 일관성 조사다.
  모델 이름에서 실행 엔진을 분리하고, Workload는 Pod가 아닌 사용자-facing 추론 서비스로
  명명한다. 실행 인스턴스에는 필요한 경우에만 모델·노드·할당 정체성을 나타내고,
  내부 전송용 Kubernetes Service는 실제 인스턴스 UID에 결속한다.
  직관적 이름은 확인된 실제 역할로 정하며, 미입증 `fast`/`efficient` 서비스 구분을
  만들지 않는다. Workload 교체는 기존 API로 새 UID 생성 후 원래 UID drain/delete를
  수행했다. 옛 이름 둘은 실제 404, 새 서비스 둘은 200/Ready/admission=true다.
  내부 전달 Service도 `chill-executor-<실제 Pod UID>`로 교체했고 이전 Service는 삭제했다.
  이전 Service 제거 후 두 번째 서비스의 실제 추론 200/실행 완료를 확인했다.
  모델·준비 Pod 명칭 정리는 pending이며 과거 evidence는 원래 UID로 보존한다.

## 다음 의존성 작업 — genesis 제외, 비서버 GPU 경로 하나

사용자 검토를 통과해 실행을 재개했다. **genesis1/2/3 측정·제어는 후순위로 보류한다.**
진행 중이던 genesis2 자기 캠페인은 8 trial 상태를 보존한 뒤 종료했으며 완료로 세지 않는다.
Orin Nano의 최초 GPU 컨테이너 생성은 Toolkit 1.20 CSV/CDI가 플러그인의 `tegra` ID를
거부하여 실패했다. 사용자가 제한된 수정에 동의한 뒤, NVIDIA가 지원하는 index 방식으로
**Orin의 플러그인만** `nvidia-device-plugin-orin-nano`에 분리했다. 공유
`nvidia-device-plugin-jetson`은 Orin을 제외하고 `OnDelete`로 두어 다른 3노드 Pod의
자동 롤링을 막았다. **공유 플러그인의 향후 자동 롤링도 꺼진 상태**이며 다음 운영 변경 때
이 사실을 반영해야 한다. 호스트 패키지·containerd·전력은 변경하지 않았다.
실제 GPU request 1의 컨테이너 생성과 CUDA Driver API 초기화가 성공했고,
`NVIDIA_VISIBLE_DEVICES=0`, GPU UUID `GPU-31ec0c91-d383-5ca3-8772-28981cf737aa`,
Driver API `12060`을 확인했다. 다른 3노드 플러그인과 기존 Orin baseline Pod의
UID·container ID 보존은 `/tmp/chill-edge-orin-plugin-cutover-proof.json`에 기록했다.
자기 테스트 Pod는 삭제했다. GPU 식별/할당만으로 프로파일 완료를 세지 않는다.
Xavier NX도 실제 UUID/명시적 선택은 확인했으나 CPython3.8용 호환 GPU 컨테이너가
`ImagePullBackOff`여서 추론하지 못했다. 불필요하게 이 경로를 확장하지 않는다.
Orin의 같은 모델은 ONNX GPU 1.23/NumPy1.26.4에서 CUDA 노드만 실행하고 CPU fallback을
차단한 실제 추론을 통과했다. 공유 runner의 SMI 조회는 실제 실패(exit6)했고, CUDA Driver
API로 교체한 `1c59650`은 비-root UID65532에서 초기화와 배치1/2 binary 추론을 통과했다.
잘못 지정한 다른 노드의 UUID는 오류로 거부했다. 증거:
`/tmp/chill-edge-orin-sdk-actual-binary-inference.json`,
`/tmp/chill-edge-orin-sdk-wrong-physical-gpu-rejection.json`.
Ponytail 리뷰와 focused Go/Python7개 테스트, `make lint && make test`는 통과했다.
Orin에 설치된 CUDA12.6/cuDNN9.20의 실제 runtime library를 이미지에 고정했다.
최종 image `4b316e0b..`는 수동 host mount/pip 없이 UID65532에서 같은 SDK 초기화와
배치1/2 실제 추론을 통과했다. 증거는
`/tmp/chill-edge-orin-vendor-complete-image-actual-sdk-inference.json`이다.
범용 ARM64 CUDA image `3dcc21dd..`의 실제 CUBLAS 실패는 보존하고 채택하지 않았다.
host 호환 계층 없이 기존 실행 선언·정책 API로 등록했다. 최초 캠페인 UID `1fe633d1..`은
Orin에 없는 `fixed` 전력모드로 계획 해석에 실패해 측정을 시작하지 못했다. 자기 객체를
기록 후 교체한 `mobilenet-v2-050-orin-nano-cuda-q1`(UID
`94446d2b-e24d-4c38-a4c1-63654a70485f`)은 현재 25W read-back과 60초 settling을
확인했지만, loaded-idle 두 시도가 `memory.peak` 부재와 workload-observer의 API
접속 timeout으로 실패했다. 실제 전력모드 변경·성공 프로파일·채택은 없었다. 근거:
`/tmp/chill-edge-orin-current-mode-campaign-child-failure.json`.
계획 해석 실패는 이제 실제 resolver 오류와 `Failed/CurvePlanResolutionFailed`로
공개되며 오류 반환을 유지해 재시도할 수 있다. 로컬 API 서버의 실패→복구 회귀는
통과했고, 실제 캠페인 `orin-gpu-plan-resolution-check`는 같은 오류 상태와 자식 0개를
확인했다(`/tmp/chill-campaign-visible-plan-failure-live-proof.json`). Dashboard result
API는 결과 참조가 없다는 이유로 404를 반환했으나, 현재는 실제 캠페인 상태와 조건을
200으로 제공하고 아직 없는 storedResult/result는 null로 구분한다. 실제 Dashboard에서
인증 후 `Failed`, 완료 0건, 실제 power-mode 해석 오류, 완료 결과 없음을 확인했다.
API 증거: `/tmp/chill-campaign-visible-plan-failure-dashboard-api-proof.json`;
화면: `/tmp/omp-sshots-15a6197993ee87ba.webp`. console image `73bace12..`,
Dashboard image `4e0c0d7b..`를 실제 rollout했다. 이는 실패 공개 경로의 검증이며
GPU 측정 성공이나 FULL 제어 검증이 아니다. focused `go test ./internal/operatorconsole/...`,
Dashboard `pnpm typecheck && pnpm lint`, 두 image build는 통과했다.
profiler ConfigMap의 image를 `7771c693..`으로 바꿔 실제 rollout했다. Deployment만
바꾼 첫 시도는 operator가 선언대로 되돌렸으며, Helm values에는 아직 반영되지 않았다.
첫 `make lint`는 통과했지만 `make test`는 timeout 응답 테스트가 기대한 504 대신
408/RequestCanceled를 받은 것으로 실패했다(`artifact://898`). 비어 있거나 이미 소비한
request body의 transport read deadline이 작업 timeout과 경합하는 원인을 수정했다.
실제 TCP GET/POST 회귀와 통합 후 `make lint && make test`는 통과했다(`artifact://950`).
Claude 에이전트의 고정 계획 수정·독립 리뷰·owner/Node UID 보완을 통합했다.
실제 profiler `4663d8d4..`로 rollout 후, 설정 변경 때문에 Failed가 됐던 기존 GPU
캠페인 두 개는 각각 기존 9 trial·stored result를 유지하며 Complete로 복구됐다.
새 Genesis 프로파일링이 아니라 저장된 근거의 상태 복구다. 근거:
`/tmp/chill-stored-plan-controller-before-fix.json`,
`/tmp/chill-stored-plan-controller-after-fix.json`.
Orin image `99f5ddfa..`는 실제 GPU 할당·UUID·설치 package 조회와 cgroup memory.current
관측을 통과했다. 실제 4개의 시각별 sample과 관측 최대값을 보존했고 테스트 Pod는 삭제했다.
근거: `/tmp/chill-orin-window-memory-actual-image-inspection.json`.
이는 kernel high-water가 아닌 측정 창의 sampled maximum이며 짧은 spike를 놓칠 수 있다.
캠페인 UID `22cbf028..`은 GPU loaded-idle 10초·전력 12/12·workload 연속 관측에
성공한 뒤 S3 commit에서 실패했다. 실제 probe는 Store DNS 해석 실패, 기존 Service
ClusterIP `10.97.177.19:9000` HTTP403 도달, 직접 Pod IP timeout을 확인했다
(`/tmp/chill-orin-evidence-actual-route-probe.json`). 에이전트의 공통 binding 수정은
새 노출·fallback 없이 기존 Service API의 할당 IP를 사용한다. profiler/console에는
자기 namespace의 그 Service 하나에 get만 허용했고 실제 can-i를 확인했다.
현재 profiler `1b190380..`, console `905240dd..`를 rollout하고 동일 25W 조건의 자기
캠페인 UID `9946bf74-4bf1-4c6e-b5f6-ab8f975633f4`를 새로 실행했다.
기본/Profiler Helm render와 `make lint && make test`는 통과했다(`artifact://980`,
`artifact://978`). Orin CPU1·batch1/2의 정식 9 trial은 Complete/ResultWritten으로
완료했고 실제 Dashboard API에서 저장 결과와 CUDAExecutionProvider의 64회 실행·
12/12 wall-power 관측을 확인했다. 근거:
`/tmp/chill-orin-q1-completed-dashboard-result.json`,
`/tmp/chill-orin-q1-actual-gpu-passport.json`.
CPU2·affinity[2,3]의 다음 캠페인 UID `072cd67a-2f19-4efc-a6a1-6ab059ccce7f`를
순차 실행 중이다. profile의 scheduler Catalog 채택과 FULL은 아직 완료 처리하지 않았다.
`make gopls-check`는 PATH에 저장소 bin을 포함한 실행으로 통과했다. 최초 실패는
그 PATH에 `rg`가 없었던 환경 문제이며 코드/검증을 우회하지 않았다.
다른 작업을 중단하거나 전력 안전 허용 범위를 넓히지 않는다. baseline-fleet/fastdds가
있으므로 전력모드 변경은 보류한다. GPU 기대 효율은 가설이며 실측으로 검증한다.
목표는 그대로 실측 근거 → SLO 우선 판단 → 실제 FULL 적용 → 실제 Dashboard 종단 연결이다.
명칭·문서 확장 등 낮은 우선순위 작업은 그 핵심 경로를 막을 때만 진행한다.

## 진행 기록 찾아보기

기록 당시 상태·실패·사용자 지시·evidence 경로는 아래에 그대로 보존한다.
새 결과는 해당 기록 파일에 남기고, 현재 상태와 다음 의존성은 이 진입 문서에 갱신한다.

1. [초기 작업 후보·ARM64 실행 이미지 검증](2026-10-13-scheduler-live-evidence-campaign-01-runtime.md)
2. [실행 선언·상시 모델 등록·설정 지속성](2026-10-13-scheduler-live-evidence-campaign-02-declarations.md)
3. [정식 측정·노드 조사·반복 trial](2026-10-13-scheduler-live-evidence-campaign-03-profiling.md)
4. [측정 근거 연결·원자 제어·실제 서비스 관측](2026-10-13-scheduler-live-evidence-campaign-04-control.md)

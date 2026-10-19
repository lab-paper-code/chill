# 외부 출처

"원문 확인"은 2026-10-19에 PDF/페이지 본문에서 해당 주장을 직접 확인했다는 뜻이다. 나머지는 검색 결과의
초록·문서 요약으로 확인했다.

## 서빙 시스템

| 출처 | 이 제안에서 쓰는 주장 | 확인 |
|---|---|---|
| [Nexus, SOSP'19](https://homes.cs.washington.edu/~arvind/papers/nexus.pdf) | 배치 b의 최악 지연 `2ℓ(b)`, `B = argmax_b(2ℓ(b) ≤ L)`, squishy bin-packing | 원문 확인(§4.1, §6) |
| [Shepherd, NSDI'23](https://www.usenix.org/conference/nsdi23/presentation/zhang-hong) ([PDF](https://www.usenix.org/system/files/nsdi23-zhang-hong.pdf)) | 주기적 계획(Herd, ILP)과 온라인 serving(Flex)의 2단 설계 | 원문 확인 |
| [Clockwork, OSDI'20](https://www.usenix.org/conference/osdi20/presentation/gujarati) ([PDF](https://www.usenix.org/system/files/osdi20-gujarati.pdf)) | DNN 실행시간의 예측 가능성, 동작마다 earliest/latest | 원문 확인 |
| [Kairos, HPDC'23](https://arxiv.org/abs/2210.05889v3) | 온라인 탐색 없이 상한 근사로 이기종 구성 선택 | 원문 확인 |
| [INFaaS, ATC'21](https://www.usenix.org/conference/atc21/presentation/romero) | 오프라인 프로파일 + 온라인 휴리스틱 variant 선택·autoscaling | 요약 |
| [MArk, ATC'19](https://www.usenix.org/conference/atc19/presentation/zhang-chengliang) | SLO-aware 비용 최소화 서빙 | 요약 |
| [AlpaServe, OSDI'23](https://www.usenix.org/conference/osdi23/presentation/li-zhouhan) | simulator 기반 배치 계획 | 요약 |
| [Clipper, NSDI'17](https://www.usenix.org/conference/nsdi17/technical-sessions/presentation/crankshaw) | 적응형 배칭(AIMD) 서빙 | 요약 |
| [DynamoLLM, HPCA'25](https://arxiv.org/abs/2408.00741) | request-type별 pool, 분(노드)~초(주파수) 계층 controller, 재구성 비용 은닉 | 원문 확인 |

## 에너지·edge

| 출처 | 주장 | 확인 |
|---|---|---|
| [Fulcrum (arXiv 2509.20205)](https://arxiv.org/html/2509.20205v1) | Jetson power mode 선택 최적화 | 요약 |
| [PowerTrain (arXiv 2407.13944)](https://arxiv.org/html/2407.13944v1) | Jetson power mode별 시간·전력 예측 | 요약 |
| [Jetson Orin power modes (nvpmodel)](https://docs.nvidia.com/jetson/archives/r36.4/DeveloperGuide/SD/PlatformPowerAndPerformance/JetsonOrinNanoSeriesJetsonOrinNxSeriesAndJetsonAgxOrinSeries.html) | 모드가 CPU 코어·GPU TPC·주파수 상한을 정함 | 요약 |

## Queueing 모델

| 출처 | 주장 | 확인 |
|---|---|---|
| [Queueing Analysis of GPU-Based Inference Servers with Dynamic Batching (arXiv 1912.06322)](https://arxiv.org/abs/1912.06322) | batch-size 의존 서비스시간 큐의 closed form | 요약 |
| [Model-driven Cluster Resource Management for AI Workloads in Edge Clouds (UMass, TAAS'23)](https://lass.cs.umass.edu/papers/pdf/taas23-ibis.pdf) | edge 가속기 공유 시 응답시간 queueing 모델 | 요약 |

## Kubernetes·제어 루프 패턴

| 출처 | 주장 | 확인 |
|---|---|---|
| [Scheduling Framework](https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/) | plugin 구조(Filter/Score) | 요약 |
| [Taints and Tolerations](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/) | NoSchedule로 scheduler가 배치 거부 | 요약 |
| [Leases](https://kubernetes.io/docs/concepts/architecture/leases/) | node heartbeat, leader election | 요약 |
| [Server-Side Apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/) | field manager 소유와 conflict | 요약 |
| [API conventions](https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md) | spec/status 분리, level-based | 요약 |
| [In-place Pod resize (1.35 stable)](https://kubernetes.io/docs/tasks/configure-pod-container/resize-container-resources) | 재생성 없이 CPU·메모리 변경 | 요약 |
| [Image volumes (KEP-4639)](https://kubernetes.io/docs/tasks/configure-pod-container/image-volumes/) | OCI image를 volume으로 mount | 요약 |
| [Kueue AdmissionCheck](https://kueue.sigs.k8s.io/docs/concepts/admission_check) | 단일 주체의 admission 판정 | 요약 |
| [Karpenter disruption](https://karpenter.sh/docs/concepts/disruption/) | consolidation, disruption budget, scheduling simulation | 요약 |
| [descheduler](https://github.com/kubernetes-sigs/descheduler) | LowNodeUtilization/HighNodeUtilization | 요약 |
| [Knative KPA](https://knative.dev/docs/serving/autoscaling/kpa-specific) | stable/panic window | 요약 |
| [Ray Serve advanced autoscaling](https://docs.ray.io/en/latest/serve/advanced-guides/advanced-autoscaling.html) | `target_ongoing_requests` | 요약 |
| [Kleppmann, How to do distributed locking](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html) | fencing token | 기존 지식(본문 미확인) |

## 관측 저장

| 출처 | 주장 | 확인 |
|---|---|---|
| [Prometheus storage](https://prometheus.io/docs/prometheus/latest/storage/) | sample당 평균 1–2 byte | 원문 확인 |
| [Prometheus native histograms](https://prometheus.io/docs/specs/native_histograms) | sparse exponential bucket | 요약 |
| [Prometheus histograms practice](https://prometheus.io/docs/practices/histograms/) | 구간별 분포 조회(명세가 이미 인용) | 요약 |
| [VictoriaMetrics single-node](https://docs.victoriametrics.com/victoriametrics/single-server-victoriametrics/) | 단일 노드 TSDB | 요약 |

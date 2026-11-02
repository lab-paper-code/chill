# Plan 2: Immutable Profiling Run and Execution Evidence

## 결과 요약 — completed
- 결정: 한 Run은 한 선택 batch, 하나의 immutable RunConfig, 한 Job과 정확히 한 Pod attempt다. 실행 증거를 만들며 scientific admission·profile publication·scheduler 결정을 하지 않는다.
- RunConfig는 exact catalog/Node UID/artifact/runtime/observer image, bounded measurement 및 metric/workload source를 동결한다. CPU request·quota·affinity 선택은 없다. Job이 recoverable executable record이며 별도 Run-record ConfigMap이나 intermediate resolution phase는 없다.
- CPU는 예약·제한·pinning 없는 공유 실행이다. ORT SessionOptions의 기본 자동 병렬성을 그대로 사용하고 실제 session thread 설정(0은 자동), cpuset/affinity, unlimited를 포함한 cgroup quota, CPU 사용·throttling을 raw 증거에 기록한다. 이 관측값은 admission이나 active/idle matching의 자원 보장 조건이 아니다. 메모리 envelope·GPU UUID/whole-device allocation·소유권 검사는 유지한다.
- Job envelope: `parallelism=1`, `completions=1`, `backoffLimit=0`, `restartPolicy=Never`, service-account token 비활성, immutable OCI images/`IfNotPresent`, hostname placement, artifact `emptyDir`. Kubernetes가 실제 admission/placement/container 결과를 소유한다.
- active는 positive batch·warmup·measured calls를 기록한다. loaded idle은 `warmupIterations=0`, `workloadMode=idle-model-loaded`, `inputGenerator=none`, `outputValidation=none`, positive settling, inference 0이며 RKNN은 첫 planned batch, 나머지는 항상 batch 1을 쓴다.
- raw writer는 worker, terminal Job/Pod/Node 사실과 finalized evidence writer는 controller다. Job success만으로 완료되지 않으며 receipt는 힌트일 뿐 독립 retrieval/strict identity verification 후 evidence reference를 발행한다.
- runtime monotonic clock이 measurement window와 대응 wall timestamps를 선언한다. metric/workload 관측은 창을 이동·축소·선택하지 않는다. receipt timestamp는 device timestamp가 아니다.
- 결과: Lattepanda CPU ORT active/idle live path. active 30 s에서 calls와 raw latency samples가 각각 5,541개, power receipts 31개·read failure 0·max gap 1.059 s였다. four-CPU quota, ORT 4/1 threads와 artifact bytes를 검증했으며 co-resident Pods 10개(6 Running)의 전력을 무시했다고 주장하지 않는다.
- 근거 커밋: `714dfd10` raw samples/preprocessor 실행, `719341eb` runtime 실패 시 observer 종료. 현행 serialization/handoff는 [Measurement Worker](../components/measurement-worker.md), 저장은 [Evidence Store](../components/evidence-store.md).
- 남은 것: 재측정은 새 Run identity다. 에너지 계산은 [Plan 3](03-profile-admission-derivation.md); live-state 확인과 실제 실행 가능성을 static compatibility에서 추정하지 않는다.
- 2026-11-02 저장 계약 변경: RunConfig v1alpha18, curve-plan v1alpha10, active runtime v1alpha9, idle runtime v1alpha7로 단일 cutover한다. 이전 quota/affinity 실행은 새 공유 실행 측정으로 자동 재사용하지 않으며 과거 원시·publication은 변경하지 않는다. 위 live 결과는 과거 four-CPU 실행의 관측이며 새 공유 실행 성능 검증 결과가 아니다.
- [Profiling index](../README.md)

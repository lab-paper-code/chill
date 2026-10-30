# Plan 8: Declared-Batch-Size Artifacts

## 결과 요약 — completed
- 결정: derived RKNN artifact의 finite declared batch set은 conversion constraint다. symbolic-batch canonical ONNX는 `BatchEnergyOptimization.spec.batchSizes`가 measurement choice를 소유한다. 정확히 하나만 사용하며 둘을 merge하지 않는다. batch sizes는 positive/unique/strictly increasing이고 sparse set도 허용한다.
- artifact input/preprocessor 현행 계약은 [heterogeneous workloads](../../serving/heterogeneous-workloads.md)을 따른다. missing input declaration은 `ArtifactInputContractMissingError`이며 controller가 artifact를 열어 probe/infer/default하지 않는다. 데이터형은 runtime/loaded-artifact 검증의 소유다.
- 첫 reconcile에 선택 set/ordered input을 immutable `curve-plan.json`의 `PlannedBatchSizes`로 기록한다. 이후 stored plan이 권위이며 current facts로 재투영하지 않는다. artifact source는 `TargetPlatform/Tool/ToolVersion`, canonical campaign source는 세 값을 빈 값으로 구별한다.
- 한 artifact는 한 execution state key/loaded-idle baseline을 가지며 idle은 첫 planned batch를 사용하고 inference하지 않는다. runtime의 selected batch 거부는 ordinary trial failure다. 현행 소진 outcome은 [Plan 7](07-batch-energy-optimization-control-path.md)의 `MinimumFound/SearchSpaceExhausted`이며 saturation 주장이 아니다.
- 실측 근거: `mobilenet-v2-050.onnx` input `input=["batch",3,224,224]`, output `output=["batch",1000]`, float32. RKNN-Toolkit2 2.3.2 artifact `mobilenet-v2-050-rk3588-dynamic-b1-2-4-8-16.rknn`은 16,238,707 bytes, SHA-256 `74328e5c931720e6ffc3ee5d0a1c6f3d71b40f84a730d8e7f36d8bbe8bd22f50`다.
- RK3588 probe: load/init return 0; batches 1/2/4는 `[batch,1000]` finite output·batch preserved, undeclared 3은 거부됐다. runtime NHWC profile inventory는 `[16,8,4,2,1]x224x224x3`; `librknnrt 2.3.2 (429f97ae6b@2025-04-09)`, driver 0.9.6, model version 6, `RKNPU v2 / rk3588`, dynamic_shape였다. 단일 probe를 다른 artifact/backend의 지원으로 일반화하지 않는다.
- 기록 소유: `model-artifacts/<modelSpec>/<artifact>/{conversion,device-probe}.json`은 첫 Run 준비, `batch-energy-optimizations/<uid>/curve-plan.json`은 첫 reconcile, `result.json`은 measurement end, `<uid>/{raw,execution}`은 각 Run이다. strict current-only readers를 쓰고 later-schema 재도출에는 새 evidence가 필요하다.
- 근거 커밋: `107ea57e` input sample-shape 명명 정리, `f8127991` batch-stage 명명 정리. [ADR 0016](../../../adr/0016-declared-batch-size-artifact-capability.md) · [RKNN exporter](../../../../build/model-artifacts/rknn/export_rknn.py).
- 남은 것: one-batch-per-artifact runtime은 자기 conversion/device/state/baseline 증거가 있는 별도 execution path가 필요하다. catalog declaration만으로 실행을 보장하지 않는다.
- [Profiling index](../README.md)

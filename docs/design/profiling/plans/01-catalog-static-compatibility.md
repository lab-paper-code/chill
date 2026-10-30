# Plan 1: Catalog and Static Compatibility

## 결과 요약 — completed
- 결정: `ModelSpec`의 immutable artifact/OCI carrier와 execution-path 요구사항, stable `DeviceClass`, trusted exact-image runtime declaration만 비교하는 read-only preflight다. 실행·배치 최적화·스케줄링 허가가 아니다.
- `internal/modelcompat`는 Kubernetes 의존성 없이 `Compatible` / `Incompatible` / `Unknown`을 계산한다. 누락·미지원 관계는 `Unknown`; 설정 I/O·strict decoding·참조/identity 손상은 candidate 생성 실패다. 호출자는 verdict를 명시적으로 확인한다.
- runtime family/backend는 요구사항이며 실제 image/provider/host 준비 상태의 증명이 아니다. 런타임 선언은 mutable tag/index가 아닌 runnable child `repository@sha256`에 묶이며 누락·불명확 플랫폼·ORT import/provider 불일치는 fail-closed다.
- artifact derivation은 source artifact, target platform, finite declared batch sizes, conversion tool/version을 소유한다. host ABI/driver/device/allocation과 runtime access는 execution fact의 소유이며 catalog 결과로 실행 가능성을 추정하지 않는다.
- 결과: 첫 CPU ORT consumer와 exact-image producer가 구현됐고 세 verdict가 exact fixtures로 확인됐다. 선언의 verification label은 producer 신뢰 경계이지 서명 또는 runtime admission 증명이 아니다.
- 근거 커밋: `ac92d0f3` firstfloor-only profiling, `80493bfd` runtime이 읽을 수 없는 file/field 이름 거부. 현행 경계는 [runtime execution](../foundations/runtime-execution/README.md), batch 소유는 [ADR 0016](../../../adr/0016-declared-batch-size-artifact-capability.md).
- 남은 것: 새 format/runtime 관계는 concrete producer와 comparison rule이 생기기 전까지 `Unknown`; live Node readiness/resource availability는 이 계획 밖이다.
- [Profiling index](../README.md) · [current evaluator](../../../../internal/modelcompat)

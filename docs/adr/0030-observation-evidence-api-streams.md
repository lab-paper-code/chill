# 0030 Observation Evidence API Streams

## Status

Accepted (2026-10-30) — 문서 계약 채택. 구현·승인 배포·리허설 완료 주장이 아니다.

## Context

[E5 증거 API 계획](../plans/2026-10-28-e5-evidence-api.md) §1~6은 CHILL 내부 사실을 CHILL API로 수집하도록 바꾼다. 기존 ObservationLog의 요청·판단·정의·관리 기록에 컨테이너 자원과 Pod/observer issue 기록을 추가해야 한다. #213의 보관된 관측은 `serving/observations.v1alpha5/`에 있고, 새 API로 그 실험도 다시 읽어야 한다.

기존 네 stream의 행 의미나 encoding을 바꾸는 작업은 아니다. 여기서 저장 버전을 올리면 현재 단일 버전 reader가 #213 보관본을 찾거나 읽지 못한다. 새 stream 두 개를 이유로 기존 실험을 폐기하거나 다중 버전 reader를 만드는 것은 [ADR 0019](0019-durable-stored-contract-compatibility.md)의 비용 원칙에 어긋난다.

## Decision

1. **같은 prefix에 stream 두 개만 추가한다.** 로컬은 `<directory>/v1alpha5/<YYYYMMDDTHH>/{resources,cluster}.<MM>.jsonl`, 보관본은 `serving/observations.v1alpha5/<YYYYMMDDTHH>/{resources,cluster}.jsonl.gz`다. 기존 `requests`, `scheduler`, `catalog`, `management`와 루트 `definitions.jsonl`의 계약·버전·객체를 유지한다. 새 stream도 기존 분 경계·torn-tail 복구·archive·삭제·append health 경로를 사용한다.
2. **버전은 `v1alpha5` 하나다.** 버전 증가, 다른 prefix로의 복사·변환, 다중 버전 reader와 구형 decoding 분기는 추가하지 않는다. 기존 불변 archive와 publication(#213~#225)을 수정·재게시하지 않는다. 이번 추가와 무관한 행 변경은 이 결정으로 승인하지 않는다.
3. **배포 이전의 새 stream 부재는 결손이 아니다.** 과거 시간에 `resources` 또는 `cluster` 객체가 없는 것은 당시 기록 기능이 없었다는 뜻이며, 그 시간의 기존 네 stream을 읽는 데 장애가 되지 않는다. reader는 모든 여섯 stream의 존재를 시간 유효성 조건으로 삼지 않는다. 원시 목록은 stream별 존재/부재를 그대로 제공하고, 필요한 stream과 실제 수집 적용 구간에 대해서만 소비자가 coverage를 판정한다. 배포 이후 부재도 무조건 0건/정상으로 바꾸지 않는다. 새 manifest·소급 채움·합성 표본은 만들지 않는다.
4. **기록과 전달의 출처는 하나다.** `resources`는 node-agent의 외부 cgroup/thermal 표본과 서버 in-process 누적값, `cluster`는 서버가 관측한 Pod/issue 사실이다. `cluster` append 성공 뒤 같은 행을 composition-root callback으로 라이브 hub에 투영한다. 실패한 append는 발행하지 않는다. core가 HTTP·Kubernetes·live를 역참조하지 않는다.
5. **읽기는 기존 로컬 우선 → 보관본 경로다.** 원시 API는 닫힌 로컬 분의 JSONL 또는 저장된 gzip을 전송하고 즉석 압축하지 않는다. 가공 조회를 없애지 않으며, 원시는 하네스 전용 권한으로 격리한다. 처리/쓰기 마감은 분리한다.

## Consequences

- 기존 #213의 한 시간을 새 API로 읽는 것이 전환 리허설의 필수 항목이다. 과거 자원/클러스터 값을 채우거나 새 GC 의미로 과거 게시값을 다시 쓰지 않는다.
- 새 stream 등록은 구현 1이 한 곳에서 맡고, 자원/클러스터 생산자는 각각 독립 파일에 붙인다. 저장 형식은 하나이고 병렬 구현을 위해 두 번째 logger나 broker를 만들지 않는다.
- GC는 `runtime/metrics`의 누적 CPU·사이클 차로 보고하며 기존 gctrace 사이클 분포와 의미가 다르다. 새 publication의 측정 규칙에 출처와 의미를 기록하되 비교 가능성 관문은 만들지 않는다.
- node-agent 전송 재시도·spool은 없고 실패 구간은 결손이다. 새 기능 배포 전 부재와 수집 실패를 같은 정상 빈 값으로 숨기지 않는다.
- 원시 전송도 이제 측정 대상 서버에서 일어난다. E5 측정 창 밖에서만 원시 내보내기와 대시보드 과거 조회를 실행하고, 한 번의 리허설에서 추가 CPU 몫을 기록한다.

## References

- [Serving 관측 명세](../spec/00_measurement.md#측정의-단위)
- [Scheduler SRS](../spec/scheduler/SRS.md): SCH-67~70, SCH-AC-52~55
- [관측 설계 §17](../design/serving/observability-split.md#17-e5-증거-api-2026-10-30): API·행·권한·세 구현 슬라이스
- [ADR 0029](0029-gateway-control-data-plane-split.md): 게이트웨이 분리는 보류이며 이 API에 router 차원을 추가하지 않는다.

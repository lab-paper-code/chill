# Observation Evidence API Streams

## Status

Accepted

## Decision

ObservationLog는 `requests`, `scheduler`, `catalog`, `management`, `resources`, `cluster` 스트림과 `definitions.jsonl`을 소유한다. 현재 저장 버전은 `v1alpha7` 하나다.

로컬 행은 `<directory>/v1alpha7/<YYYYMMDDTHH>/<stream>.<MM>.jsonl`, 정의는 `v1alpha7/definitions.jsonl`에 둔다. 보관본은 `serving/observations.v1alpha7/<YYYYMMDDTHH>/<stream>.jsonl.gz`다. 같은 분 경계, torn-tail 복구, 보관과 append 상태 경로를 사용한다.

원시 API는 닫힌 로컬 분을 우선하고 보관본을 읽는다. JSONL 또는 저장된 gzip을 전송하며 즉석 압축하지 않는다. 원시 읽기는 하네스 전용 권한으로 격리하고 처리 마감과 전송 마감을 구분한다.

스트림별 존재 여부와 coverage를 공개한다. 필요한 수집 구간의 부재나 읽기 실패를 정상 0건으로 채우지 않는다. 모든 스트림의 존재를 공통 시간 유효성 조건으로 삼거나, 기록 기능이 없던 구간을 합성 표본으로 채우지 않는다.

`resources`는 node-agent의 외부 cgroup 및 온도 표본과 서버 내부 누적값을 기록한다. `cluster`는 서버가 관측한 Pod와 observer issue를 기록한다. `cluster` append 성공 뒤 같은 행을 composition-root callback으로 라이브 hub에 전달한다. 실패한 append는 발행하지 않는다.

GC는 `runtime/metrics` 누적 CPU와 사이클의 차로 보고한다. 측정 결과는 이 출처와 의미를 보존한다. node-agent 전송 실패 구간은 결손이며 재시도나 spool로 감추지 않는다.

### 판단 근거의 저장

용량 기록은 전체 node-plan 목록을 유지한다. 선택한 target과 keep에는 단계별 전력모드, loaded state, 요청 전력, 프로파일 참조와 예측 동작 시간 및 에너지를 투영한다. 선택한 예측의 0W와 0J도 기록한다. 해당 선택 단계에서 생략한 loaded state는 빈 multiset이다.

`capacity.missingCells`는 판단별 중복 없는 누락 셀 표다. 에너지 비교와 제외 후보는 같은 표의 `missingCellRefs`를 사용한다. 참조는 0부터 시작하는 정수 인덱스이며 순서가 결정적이고 중복이 없다. 비교 상태는 `decidable`, `undecidable`, `not-applicable`로 구분한다.

누락 셀은 필요한 Node, 단계, 동작 종류, 전력모드와 loaded state 조건을 식별한다. 비교 불가 후보와 검색 제외 수를 보존한다. 실제 planner 상태를 바꾸거나 누락 비용을 0으로 대체하지 않는다.

## Consequences

한 저장 경로가 원시 API, 가공 조회와 라이브 발행의 출처다. 서버의 요청 처리와 제어가 같은 프로세스에 있으므로 평가 측정 창 밖에서 원시 내보내기와 대시보드 과거 조회를 실행한다.

기존 과학 원시와 게시물은 수정하거나 재게시하지 않는다. 현재 단일 버전 reader와 작업용 객체 정리는 [Durable Stored-Contract Compatibility](0019-durable-stored-contract-compatibility.md)를 따른다.

요구사항은 [관측 원시 기록 내보내기](../spec/scheduler/SRS.md#관측-원시-기록-내보내기), [외부 자원과 온도 관측](../spec/scheduler/SRS.md#외부-자원과-온도-관측), [Pod와 장애 사건 기록](../spec/scheduler/SRS.md#pod와-장애-사건-기록), [읽기 전용 전력계 선언](../spec/scheduler/SRS.md#읽기-전용-전력계-선언)을 따른다.

수용 기준은 [완결 원시와 보관본 조회 검증](../spec/scheduler/SRS.md#완결-원시와-보관본-조회-검증), [외부 자원 표본과 누적값 검증](../spec/scheduler/SRS.md#외부-자원-표본과-누적값-검증), [Pod와 장애 기록 후 발행 검증](../spec/scheduler/SRS.md#pod와-장애-기록-후-발행-검증), [읽기 전용 전력계 설정 검증](../spec/scheduler/SRS.md#읽기-전용-전력계-설정-검증)을 따른다. 상세 구조는 [관측 설계](../design/serving/observability-split.md)가 소유한다.


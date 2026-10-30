# 0031 Configuration Ownership and Freezing Paths

## Status

Accepted — 2026-10-30. 문서 결정이며 구현·측정 완료를 뜻하지 않는다. **2차 검토(fusion: 패널 3모델+판정) 반영**.

## Context

코드의 숫자를 모두 설정으로 풀면 프로토콜까지 운영 정책이 되고, 하나의 설정 계층에 모으면 serving·측정·하네스의 책임이 섞인다. 반대로 측정 조건을 상수로 고정하면 Run을 재현하기 어렵다. AGENTS.md는 측정·판정 값의 설정화, 선택값과 출처의 불변 실행 기록, 단일값 정책 필드 금지를 요구한다. 현재 settings API와 관측 scheduler stream, 측정 CR/frozen Run, E5 config/binding이 이미 있다.

## Decision

1. A는 실제 소유자에게, B는 실제 registry·Node·외부 endpoint·잔존 namespace만 기존 환경 입력으로 옮긴다. C는 고정 방법론/패키징, D는 기존 설정 유지다. 기본 values를 비우지 않고 자명한 수식·sentinel·enum을 기계 치환하지 않는다.
2. SchedulerSettings 추가는 capacityGridSteps=20·capacityBisectionWidth=.005·capacityTailSamples=100의3개다. CapacityInput/용량 행에 포함하며 기본값도 새 κ 키로 재계산한다. 판단 적용값의 권위는 Decision.Settings, PUT 사전/후속 행은 유지한다. 초기·외부 설정은 사용 Decision으로 해석하고 가짜 초기 행·refresh 합성 행·별도 provenance 저장소는 없다.
3. 기존 cluster/server-start에 queueLimit·sourceIntervalNanos·profileIntervalNanos만 보강한다. 기존 Pod identity·run()의 started·append 실패 기동 거부를 재사용한다. 별도 scheduler startup은 없다. timeout/directory/control scope·Node cadence·attempt overhead는 기존 설정 D, power1분은 C다. resources payload는 바꾸지 않는다.
4. BEO.spec.pointPolicy.confidenceLevel(십진 문자열, 생략 "0.95")이 해당 측정의 point·serving baseline·energy CI를 함께 소유한다. 독립 idle CR 정책은 유지한다. Profile과 메시지는 실제값을 기록하며 profile-confidence CLI/values/생성자 경로는 삭제한다. 기존 immutable spec/실행·결과 참조를 재사용한다. reuse는 기존 요청 annotation의 minimumMeasurementSeconds=30·minimumTrials=3을 확장하고 결과·정책 identity에 기록한다.
5. 기존 CR trial/target 입력을 재사용하고 하한2 허용 확대를 통계 유도→저장→재로딩→serving까지 완결한다. 기본 trial3/target5는 유지한다. occupancy는 기존 duration/rate와 seed만 사용한다. 하네스는 기존 binding에 잔존 실험 정책을 동결하고 새 delay/poll/closure/G0 count knob는 만들지 않는다. 기본값은 변경 전 실제 계산·실행 함수와 비교한다.
6. 증거 API 승인 배포·리허설·하네스 cutover·대시보드 브라우저 수용 기록 완료와 의존 캠페인 부재 뒤 시작한다. 순서는 전환 후 재산정→측정 정책→용량 settings/reuse→기존 기록→잔존 하네스 A/B→통합 소유자 단일 검증이다. 저장 변경은 캠페인 사이 한 번 모은다. ADR0030의 v1alpha5·archive/publication #213~#225는 수정/삭제/재게시하지 않는다. 과거 새 필드 부재는 미기록 사실이며 소급 default하지 않는다. ADR0019 정리는 영향 작업용 CR/파생 객체만 열거해 운영자가 수행한다. 과학 원시 보존과 혼동하지 않는다. 단일 reader를 유지하고 dual decoder·migration·alias·shim·새 hash/바이트 관문은 없다.

## Evidence and Consequences

기존 PUT은 사전 append→persist→refresh→후속 append다. 사전 실패는 변경 전 중단하지만 저장 후 refresh 실패는 요청/적용 불일치, refresh 후 후속 기록 실패는 HTTP 오류여도 적용되었을 수 있다. 이후 Decision으로 해석하고 충분한 증거가 없으면 미확인이다. 실제 보존하지 않은 ConfigMap UID/RV는 주장하지 않는다. `hack/e5/e5.py:1294`는 config/schedulerSettings/sources/schedule을 binding에 보존한다. 파일 overwrite가 가능한 현재 binding을 물리적 write-once 저장소라고 부르지 않는다. 완결 binding은 해당 실험에서 변경하지 않고 다음 실행은 새 binding을 만드는 계약으로 동결한다.

기존 소유자를 재사용하므로 추가 설정 계층은 없다. 대신 모든 값의 전달과 기록을 끝까지 연결해야 한다. 기존 CLI와 Helm 기본값이 다른 경우 양쪽을 그대로 보존하며 값이 같다고 꾸미지 않는다. 역사 스크립트는 재설계하지 않고 삭제 후보로만 남긴다. 실제 삭제에는 별도 사용자 승인이 필요하다. 필드·기동 행·병렬 slice와 회귀 기준은 [상세 설계](../design/serving/configuration-hardcoding.md), 범위·열린 점은 [계획](../plans/2026-10-30-configuration-hardcoding.md)이 소유한다.

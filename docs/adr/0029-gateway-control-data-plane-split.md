# 0029 Gateway Control/Data Plane Split

## Status

**Deferred — 배포·G3 진행 보류(2026-10-28 2차 검토).** 정리 완료, 설계만 보존(2026-10-30): `e947cd5e`의 미사용 분리 구현·빌드 태그를 제거했으며 T5 live는 유지한다.
분리 구조의 채택·운영 적용·성능 인증을 뜻하지 않는다. 아래 Decision은 확정 일정이
아니라 **재개 시 지킬 안전 불변식**이다.

## Context

현재 CHILL은 이미 모델 실행을 여러 native 인스턴스로 분산한다. 중앙화된 것은
ChillServer 한 프로세스의 요청 인증·입력 검증·가중 배정·본문 중계·물리 점유·관측
기록이며, 같은 프로세스가 계획·관측 복원/조회·운영 작업도 수행한다. 제안서는
관측 경로 CPU 증가와 기동 복원 OOM을 제어 측 작업과 서빙 장애가 결합된 사례로
기록했다. 이는 이번 문서 작업에서 재측정한 결과가 아니다.

라우팅·전환 계약은 중앙 본문 중계 자체를 요구하지 않는다. 보존해야 할 것은
최초 수신 기준 시간, 원시 입력 신뢰 경계, Workload 정의와 정확한 실물 identity,
가중 배분, 배정 전 기록, 취소 뒤 물리 완료 추적 및 안전한 source 퇴역이다.
현행 `definitionID`, native `DecisionID`, 선언 ConfigMap resourceVersion은 각각
다른 의미이며 독립적인 배분표 버전/라우터 ACK가 이미 존재하는 것은 아니다.

G0(#203)의 동결된 gateway 판정은 PASS다(`binding.json`의 gatewayVerdict, CPU 최고
1.60/6코어, limit 대비 0.266, throttle 0). 이는 해당 부하점의 비병목 확인이며 최대
G나 여유 인증이 아니다. 한편 #213 완료 요청의 `gatewayTailNS`(`elapsedNanos −
nativeElapsedMS`) PLC p95는 계획기 탐색 중 37~46ms, 탐색 밖 7~8ms였다. 전체 완료
p95는 16~18ms로 SLO 안이다. 이것은 제어 작업과 요청 꼬리의 상관이며 순수 gateway
처리시간이나 분리 효과의 인과 증거가 아니다. 원인(CPU 경쟁·GC·전달 구간)은 미확정이다.

## Reopen conditions

재개는 E5 종료·결과 표 기입 뒤 다음 중 하나가 있고, 기존 경로의 직접 수정이나 작업
동시성 축소(예: κ 계산 병렬도)로 해결되지 않을 때 결정한다.

1. gateway에 귀속되는 G 확인(SCH-AC-46) 실패.
2. 제어·조회·복원 작업의 간섭이 spec 측정 표의 지연·성공률을 훼손한다는 근거.

재개 전에 연구 질문, 연결할 spec 측정 행, 비교 조건과 합격 기준을 명시한다. 첫 검증
구성은 **control 1 + router 1**이다. router N>1은 router 1개의 한계 또는 라우터 수
자체의 연구 필요가 확인될 때 별도로 승인한다.

근거 원본: `O/intent/gateway-20261027/{proposal,arch-map,research}.md`,
`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`.
요구사항은 [scheduler SRS §8](../spec/scheduler/SRS.md)의
SCH-60~66 / SCH-AC-47~51, 상세 설계는
[gateway-split](../design/serving/gateway-split.md)에 둔다.

## Decision (재개 시 지킬 안전 불변식)

다중 라우터 온라인 가입·제외, 영속 membership, 프로세스 세대 custody·자동 Pod 교체
복구, router별 quota 분할, 실행 전 거절 뒤 자동 재배정은 **기본 채택 사항이 아니다**.
첫 캠페인은 고정 구성이며 장애 시 신규 유입을 닫고 캠페인 실패와 미확인 물리 점유를
보존한다. assigned 선행 기록, 물리 완료 확인, 안전한 reset은 축소하지 않는다.

1. **판단은 중앙, 전달은 분산한다.** control은 같은 서버 조립 소스를
   `gatewaycontrol` build tag로 만든 별도 `chill-control` 실행 파일이고, 데이터
   평면은 별도 `cmd/chill-router`다. default-off 배포 플래그는 추가하지 않는다.
   control은 하나의 전역 계획기, 관측기, 실물 장부·예약, 운영 작업과 관리/조회 API를 소유한다. 여러 router는
   하나의 추론 Service 뒤에서 인증·입력 검증·미배정 큐·SWRR·점유와 요청 기록을
   소유하고 native의 Pod 전용 Service/EdgeMesh로 직접 전달한다. 요청마다
   control picker를 호출하거나 본문을 control로 중계하지 않는다.
2. **배분표는 버전이 붙은 불변 snapshot이다.** control은 Workload UID·정의 ID·
   요청 수명·Pod UID/container·endpoint·가중치·정책 참조를 `planVersion`으로
   묶어 push한다. router는 전체를 검증하고 원자적으로 교체한다. 한 요청은
   한 버전만 사용하고 요청 행에 `routerID`와 `planVersion`을 남긴다.
3. **적용 확인은 두 단계다.** 먼저 native의 실제 정책 `DecisionID` ACK를
   확인한 뒤 배분표를 보낸다. 각 router가 실제 교체한 후 보낸
   `applied(planVersion)`을 모두 확인해야 route 적용 완료다. 전송 성공이나
   Dashboard 표시용 순번은 적용 증거가 아니다. 라우터 가입·제외도 이 장벽에
   반영하며, 전체 라우터의 동시 원자 전환은 약속하지 않는다.
4. **퇴역은 모든 소유 라우터의 물리 점유로 막는다.** source 신규 배정 차단과
   applied 장벽 후, 그 source에 배정할 수 있었던 모든 router의
   `outstanding=0` 및 기존 native 물리 완료 증거를 확인해야 drain 완료·stop·
   remove로 진행한다. timeout, 연결 단절, readiness 제외를 점유 0으로
   대체하지 않는다. 수락된 미배정 요청·실물 장부·범위 예약은 유지한다.
5. **T5 WebSocket 메커니즘을 재사용한다.** 인증된 TLS 연결, bounded 송수신,
   순번·재동기화·heartbeat의 전달 방식을 공유하되 Dashboard의 읽기 전용
   feed/권한과 router의 배분표/ACK 권한은 분리한다. T5가 구현 완료됐다고
   전제하지 않으며 G2에서 공통 전송부를 연결한다. 배분표와 점유/완료 증거는
   손실 가능한 화면 지표로 취급하지 않는다.
6. **라우터별 기록을 기존 보관/reader 경로로 합친다.** 각 router의 독립
   assigned/final·catalog stream을 시간별 파일과 근거 저장소에 보관하고,
   λ·d·E5·Dashboard reader는 `requestID`로 `final > open > assigned` 병합한다.
   로컬/archive 우선순위는 시간뿐 아니라 router 차원에서 적용한다. 출처·공백과
   참여 router 목록을 남기며 누락을 무유입으로 바꾸지 않는다.
7. **stale은 조용히 통과시키지 않는다.** 적용 미확인/연결 상실 router는
   신규 유입 gate와 readiness를 닫고, 이미 시작한 요청의 물리 완료는 계속
   추적한다. stale 유입 또는 기록 완전성 미확인은 실험 무효/검증 실패다.
   native admission이 최종 상한이며 초기 router별 점유 한도는 보수적으로
   나눈다. 정확한 전역 lease 할당기는 이번 범위에 추가하지 않는다.

## Rationale and rejected alternatives

| 대안 | 기각 이유 |
|---|---|
| 기존 ChillServer의 replicas만 증가 | 프로세스 내부 큐·SWRR credit·outstanding·route 교체와 제어 작업을 그대로 복제하면 정합성과 단일 전역 계획기 계약이 깨진다. |
| 공유 Kubernetes Service에 native 선택 위임 | 연결 단위 무가중 선택은 CHILL의 Workload별 가중치·실물 identity·전환/drain 의미를 표현하지 못한다. |
| 클라이언트가 직접 native 호출 | 일반 클라이언트에 인증·원시 입력 검증 경계와 최초 수신 기록을 맡길 수 없다([ADR 0021](0021-raw-sample-preprocessing-boundary.md)). |
| native에 router 기능 추가 | runtime 7 이미지 재빌드와 실행 조건별 프로파일 재측정이 필요하다. 기존 firstfloor 배처/정책을 바꿀 이유가 없다. |
| 요청마다 중앙 picker 조회 | payload 중계를 줄여도 요청별 판단 병목·제어 가용성 의존이 남는다. |
| Istio/Ray/KServe·새 broker/저장소 도입 | 현 요구에는 기존 Go 서버·T5 전송·관측 보관 경로로 충분하다. 별도 운영 경계는 추가하지 않는다. |
| 전역 동시 원자 전환 또는 처음부터 quota lease | 비동기 router 적용의 혼합 구간을 숨기거나 불필요한 조정기를 도입한다. 버전별 증거와 native 최종 admission으로 시작한다. |

## Consequences

- 제어 작업의 CPU/GC/기동 복원과 추론 프로세스 장애 경계를 분리한다. 처리량·SLO
  개선 폭, control 중단 시 가용 시간이나 무중단은 측정 전 보장하지 않는다.
  채널 상실 시 안전을 위해 신규 유입을 닫으므로 무제한 오프라인 서빙도 아니다.
- 운영자는 목표 배분표와 라우터별 applied/신선도·물리 점유·가입 상태를 구분해야
  한다. 혼합 버전 구간은 정상적으로 발생할 수 있으나 시각과 요청 버전으로
  공개하며, 미확인 상태에서 source를 정리하지 않는다.
- 각 router의 SWRR는 로컬이다. 짧은 창의 전역 분배 오차와 보수적 점유 분할의
  유휴 용량이 생길 수 있으므로 두 라우터 G/SLO·전환·장시간 시험으로 평가한다.
- 기록 소스 수가 늘어난다. namespace 충돌 방지, archive 보존, 재시작 전 미완료
  복구, 모든 reader/수집기의 router별 소스 선택을 한 번에 이행해야 한다.
  구형 단일 gateway 자료는 원래 형식으로 해석하며 split 검증 결과로 간주하지 않는다.
- native 이미지, Pod 전용 Service와 EdgeMesh, firstfloor 배칭·마감 정책, 중앙
  장부/예약 및 E5 분모는 변경하지 않는다. 새 CRD·DB·외부 프레임워크는 없다.
- 적용 순서는 **E5 완료·표 기입 → `e947cd5e` 미사용 분리 코드 정리와 단일 source
  evidence API 이행 → 재개 조건 판단 → 승인된 최소 분리 검증 → 별도 cutover**다.
  E5 종료 자체는 G2/G3 개시 조건이 아니다. 현 E5는 단일 게이트웨이와 G0 통과점을
  조건으로 보고한다.

## Change record

- 2026-10-27: G1 draft 작성. 문서 3파일만 변경하며 코드·클러스터 변경, 빌드·테스트 실행은 하지 않는다.
- 2026-10-28: T5 전송부를 `internal/serving/live`로 분리한다. 이 component는
  Serving/Kubernetes를 역참조하지 않는 DTO·유한 hub·초별 집계·WebSocket 전송을
  소유하고 `serving-httpapi → serving-live`만 허용한다. composition root가 기록
  성공 후 projection을 발행한다. `.go-arch-lint.yml`과 coverage mapping을 함께
  갱신하며 이 허용은 G2 배포 또는 E5 실행 경로 변경을 승인하지 않는다.
- 2026-10-28: G2의 `serving-gateway` component는 `serving-core`,
  `serving-firstfloor`, `serving-live`에만 의존한다. core는 전송/HTTP/Kubernetes를
  역참조하지 않고 불변 배분표·신선도 gate·외부 물리 점유 callback을 소유한다.
  composition root가 native ACK-only source, router 적용 장벽, native 진행·상태
  증거와 router별 reader를 연결한다. `.go-arch-lint.yml`과 coverage mapping을
  함께 갱신한다. 별도 control 빌드와 router는 차트에 연결하지 않는다.
- 2026-10-28: 2차 검토(fusion, 패널 3모델 + 판정)로 **Deferred**. `e947cd5e` 확인 사항:
  표 포인터가 바뀌면 Receive~Accept 요청을 `GatewayPlanChanged`로 거절
  (`internal/serving/runtime.go:306-307`, 설계 §2와 충돌); progress가 소유 router가
  아니라 노출된 모든 router로 전달(`gateway/control.go:680-707`); progress마다
  membership 전체 직렬화·Sync(`:199-227,698-701`); 같은 Pod 세대만 custody 인수
  (`recovery.go:24-41`)로 새 PodUID 교체 경로 결손; `ArrivalCounts` 운영 소비자 없음;
  기본 모드에도 적용되는 공유 `Runtime` 변경(`runtime.go:1208`). E5 종료·표 기입 뒤
  다음 서버 재빌드 전에 분리 전용 실행 경로·패키지·Runtime 분기·callback·reader·도착
  집계를 사용처 단위로 제거한다(기계적 전체 revert 아님). T5 `26c3bb76`과
  `internal/serving/live`는 유지한다.

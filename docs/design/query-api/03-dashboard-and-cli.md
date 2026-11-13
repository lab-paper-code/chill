# 운영 조회와 Measurement CLI

요구사항은 [Dashboard 명세](../../spec/dashboard/SRS.md), 웹 구조는
[Dashboard 구현](../dashboard/README.md)이 소유한다. 여기서는 현재 CLI와
조회/요청/증거 경계를 설명한다.

## 운영 의미

운영자 선언, 실제 관측, 측정 모델, 제어 판단과 실행 결과를 구분한다.
`Ready`만으로 SLO 충족을 주장하지 않는다. 조회 projection은 원본을 결합하며
별도 영속 truth를 만들지 않는다. 설치와 업그레이드는 Helm/GitOps 수명주기다.

## Measurement task CLI

```sh
kubectl chill measurements submit -f measurement.yaml -n chill-system
kubectl chill measurements submit -f measurement.yaml -n chill-system --dry-run=server -o json
kubectl chill measurements list -n chill-system
kubectl chill measurements show TASK_NAME -n chill-system -o json
kubectl chill measurements delete TASK_NAME -n chill-system --uid OBJECT_UID --if-match RESOURCE_VERSION -o json
kubectl chill profiles list -n chill-system -o json
kubectl chill profiles adopt TASK_NAME -n chill-system -o json
kubectl chill profiles unadopt TASK_NAME -n chill-system --if-match CONFIG_VERSION -o json
```

기존 BEO manifest가 요청 계약이다. CLI와 Dashboard는 같은 인증된 ChillServer
HTTPS를 사용한다. Submit은 production Query preflight 뒤 native create를 한 번
요청한다. 거절/미가용 preflight는 아무것도 만들지 않는다. 정상 접수는 HTTP 202와
`request:"Accepted"`이며 trial 완료를 기다리거나 자동 재시도하지 않는다.

`--dry-run=server`는 `dryRun=All`로 preflight/native 검증을 수행하고 저장하지 않는다.
응답은 HTTP 200, `request:"NOT_SUBMITTED"`다. 목록은 namespace의 pending/failed/
complete 객체를 포함하며 `-A`를 지원하지 않는다. Show는 기존 Query 결과를
보존하고 응답 한도는 결과 64 MiB와 task/status envelope 4 MiB다.

| Route | 계약 |
|---|---|
| `GET /api/v1/namespaces/{namespace}/batch-energy-optimizations` | Native 목록과 UID/resourceVersion |
| `POST /api/v1/namespaces/{namespace}/batch-energy-optimizations` | Preflight 뒤 create; dryRun 지원 |
| `DELETE /api/v1/namespaces/{namespace}/batch-energy-optimizations/{name}?uid={uid}` | If-Match 필수, owned evidence 영구 삭제 뒤 native 삭제 요청 |
| `DELETE /api/v1/scheduler/profiles/{namespace}/{name}` | Scheduler ConfigMap configVersion을 If-Match로 전달 |

## 삭제와 채택 안전

Measurement 삭제는 소유 raw/result S3 객체를 **영구 삭제**한다. 공유/현재 참조를
먼저 해제하고 사용하지 않는 profile만 안전하게 unadopt한다. S3와 Kubernetes
전체를 묶는 원자 transaction은 없다. 일부 evidence 삭제 뒤 native 객체가 남을 수
있다. Finalizer/GC가 실제 객체 소멸을 결정하므로 `DeletionRequested`는 소멸 완료가
아니다. CLI는 부분 실패를 보존하고 nonzero로 종료하며 자동 재시도하지 않는다.

`--uid`와 `--if-match`는 목록에서 얻은 원래 값이다. CLI가 최신 버전으로 바꾸거나
덮어쓰지 않는다. `deletedEvidence`는 정확한 cleanup key이며 실제 존재했던 객체
개수가 아니다. 이미 없는 owned key는 멱등 처리한다.

삭제 가능 범위는 현재 observedGeneration의 완료 result 또는 source가 정한
terminal 전환/재시도 소진 실패다. Pending, Measuring과 복구 가능한 계획 해석 실패를
delete로 취소할 수 없다. 공유 산출물과 이미지는 cleanup 밖이다.

| 오류 | 보존할 사실과 다음 행동 |
|---|---|
| `MeasurementEvidenceDeletionIncomplete` | 일부 key는 이미 삭제됐을 수 있음; 같은 UID/버전 재시도 가능 |
| `MeasurementDeletionIncomplete` | Evidence cleanup 완료, native 삭제 확인 실패; 신원 조회 |
| `MeasurementAuditIncomplete` | 변경 접수 가능, 최종 감사 확인 실패; 원본 조회 뒤 판단 |

Profile adopt/unadopt는 별도 명시 요청이다. Unadopt는 persisted/accepted controls가
모두 OFF이고 활성 instance나 frozen/in-progress transition이 없어야 한다.
선택과 미사용 preparation만 제거하고 다른 설정/evidence/serving Pod는 유지한다.
`Removed`/`Unselected`는 persisted/loaded false일 때 성공하며 물리 종료를 뜻하지 않는다.
설정 저장 뒤 catalog refresh가 실패한 `Pending/ProfileUnloadFailed`는 실제 loaded
상태를 보존하고 nonzero 종료한다. 채택도 거절/pending을 성공으로 출력하지 않는다.

## 인증과 접속

```sh
kubectl chill profiles list -n chill-system \
  --serving-url https://CHILL_SERVER_HOST:8080 --serving-ca-file chill-server-ca.pem \
  --token-file caller-token -o json
```

기본 URL은 `https://SERVICE.NAMESPACE.svc:8080`, Service는
`chill-serving-entrypoint`다. 접근 가능한 HTTPS origin과 hostname 일치 인증서를
사용한다. ChillServer CA는 API-server CA와 별개다. CA 파일이 없으면 system roots를
사용한다. `--token-file`은 kubeconfig bearer를 대체한다.

API-server client certificate만으로 ChillServer에 인증할 수 없다. CLI는 인증서,
impersonation/auth plugin을 전달하지 않으며 TLS 검증을 끄거나 credential redirect를
따르지 않는다. HTTP 401은 명령 실패다.

Measurement/profile, standalone preflight와 runs show는 같은 owner 접속 플래그와
검증된 bearer transport를 사용한다. `--query-service`나 Kubernetes 직접 변경
우회가 없다. Preflight `resolves:false`는 구조화 이유를 출력하고 nonzero 종료한다.

공통 플래그는 kubeconfig/context/namespace와 table/json이다. Submit/preflight는
명시 namespace, manifest namespace, kubeconfig 순서이며 다른 명령은 kubeconfig가
기본이다.

## System, Inventory, Run과 Evidence Passport

System은 ChillSystem spec과 conditions를 분리한다. Inventory는 DeviceClass selector와
live Node의 Ready tri-state, unschedulable, ModelSpec 선언 및 각각의 조회 시각/
resourceVersion을 보존한다. 원자적 cross-resource snapshot이나 compatibility 판정으로
표시하지 않는다. DeviceClass.status에 Node 집계를 복제하지 않는다.

Run 목록은 원래 spec/status/generation을 보존한다. 기본 namespace는 kubeconfig이며
Run 목록의 `-n`/`-A`는 BEO 목록 계약과 구별한다.
`runs show`는 ChillServer를 통해 Query가 exact-read한 bounded Passport를 사용한다.
미완료 evidence는 HTTP 202 metadata와 null로 남는다.

Passport는 원시 latency/power 배열, endpoint와 credential을 전달하지 않는다.
Workload evidence의 연속성, window, Pod 수/변화/환경을 투영하며 과학적 admission
단계를 추가하지 않는다. 실행 완료, evidence 검증과 운영 profile 채택은 별개다.
공통 standalone Run 생성 표면이나 미구현 raw export를 지원한다고 암시하지 않는다.

관리자가 unbound viewer 권한 binding을 소유한다. Query의 auth-delegator는 caller
TokenReview/SAR 검증에만 쓰며 Query SA 신원이 사용자 권한을 대신하지 않는다.

# 관측 분리와 E5 증거 API

[채택한 이행 단계](../architecture-rethink/README.md)의 M1/T4 설계다. ChillServer는 로컬 버퍼와 evidence store의 같은 시간 세그먼트로 조회·오프라인 표 산출에 답한다. [M0](decision-record-diet.md)가 전제이며 T4 원본은 [scheduler SRS](../../spec/scheduler/SRS.md)의 `SCH-42~47`이다. 코드 반영은 배포·클러스터 검증 완료가 아니다.
현행 E5 계약은 [17장](#17-e5-증거-api-2026-10-30)의 SCH-67~70 / SCH-AC-52~55와 [ADR 0030](../../adr/0030-observation-evidence-api-streams.md)이다. `v1alpha5`를 유지하며 과거 버전 삭제나 7·15장의 호스트/S3 직접 회수를 새 E5 수집 경로로 쓰지 않는다. 이 문서 변경은 코드·배포·리허설을 수행하지 않는다.

## 1. 측정한 문제
E1 재실행(2026-10-19, rev35, `v1alpha4`, [workload-pools](workload-pools.md)) S3 `serving/` 636창·659MB와 2req/s/무부하 1분 창 분석이 근거다.
| 문제 | 근거 |
|---|---|
| PVC 273MiB/h | soak 2req/s `du -sk`; 요청7,199×27.9KB=200.6MB + 판단360×185KB=66.7MB + 전력7,200×1.9KB=13.8MB ≈268MiB/h |
| 요청27.9KB | 약10사건/요청, assignment62%·그 안 profileEvidence19참조가 stream49%, nativeProgress13%; 필요한 사실을 한 행으로 모으면989B(gzip138B) |
| 판단185KB | candidates97KB, snapshot70KB(costs27.7/options13.6/baselines12/workloads11.3), keep/target 각각7.7KB; 연속 costs/baselines/workloads는100% 동일 |
| 무부하45MB/h | 판단87KB×360와 전력; 연속 판단은 시각만 다르고 전력1.9KB 중 표본763B |
| 누적 이력 비용 | M1 전 업로드 후 로컬 미삭제·전체 decode/census(기동27~31s `[추론]`); 워크로드 OOMKill2회(D-11) 뒤 접수 겹침 창만 읽음(b68570c). firstfloor 재독으로 재시작마다11~12MB 반복 기록은 `fix(serving): stop re-recording firstfloor progress after restart`로 수정 |

## 2. 범위
M1은 요청 행·불변 ID·S3 시간 세그먼트/로컬 버퍼·범위 읽기·오프라인 표, T4는 보존 위치 투명 조회·삭제 정의·B1~B5 projection/예상 시간이다. Prometheus/native histogram/ServiceMonitor/PodMonitor/PromQL relay·Dashboard PromQL은 제외한다. 측정 표는 TSDB를 쓰지 않고 Dashboard는 ChillServer API를 읽는다.

## 3. 요청 기록
### 3.1 행
`requests`는 행 객체이며 `kind=assigned|final`. reducer는 `requestID`별 `final > 메모리 열린 행 > assigned`를 쓴다. 열린 행은 assigned에 접수 단계까지 더한 값이다.
| 행/묶음 | 필드·규칙 |
|---|---|
| assigned | `requestID,workloadUID,definitionID,receivedAt,lifetimeNanos,dispatchedAt,node,instance,profileID`(약300B). 배정 시 append 실패는 배정 rollback |
| final 식별·결과 | `requestID,workloadUID,definitionID,node,instance,profileID,decisionID,outcome,error,executionComplete,clientSpanNanos` |
| final 시각 | `receivedAt,lifetimeNanos,elapsedNanos`(응답 종료), `validatedNanos,acceptedNanos,dispatchedNanos,queuedNanos,runningNanos,executedNanos,exchangeNanos`는 receivedAt 기준 오프셋. 미도달은 비움; 두 경로가 같은 단계를 알리면 먼저 접은 시각, 응답 종료만 마지막 값 |
| final 배치 | 마지막 backend 진행의 `batchID,batchSize,queueMS,serviceMS,backendMS,preprocessMS,postprocessMS,firstTokenMS,units,nativeElapsedMS` |
`queuedNanos/runningNanos`는 닫기 전 firstfloor `backend_queued/backend_running`에서 온다. final 때 빈 값은 열린 행의 마지막 실행 배치 finished(교환 결과/로그)에서 같은 worker 시계로 채운다. 시작=`At−nativeElapsedMS+queueMS`, 큐 진입=`At−nativeElapsedMS`(본문 읽기/decode만큼 로그보다 이르다); `queueMS−(runningNanos−queuedNanos)=0`.
profile 본문(근거/confidence/조건 artifact·실행 경로)은 `profileID`, SLO·이름은 `definitionID`로 해당 시간 catalog에서 읽는다(`WM-27`). 지연은 `aggregate` 규칙을 유지하며 `delivery_failed,incomplete,canceled` 제외; 실행 완료 때만 잔여=`elapsedNanos−dispatchedNanos−nativeElapsedMS`(`SCH-31`,`WM-28`).
기존 RequestEvent를 열린 행에 접고 assigned에서만 append한다. Runtime의 `releaseLocked` 또는 살아 있는 호출 없는 Response(접수 전 거절)에서 `RequestClosed(id)`→final append→열린 행 삭제다. 열린 행 없는 사건은 버리고 수신/검증/접수는 메모리만 사용하므로 거절은 observationError가 맡는다. 열린 행 수는 처리 중 요청 수; final989B(E1 실제121건)+assigned약300B≈요청당1.3KB다.
### 3.2 재시작
배정 전 수신/검증/큐는 crash 때 손실을 수락했으며 loadgen requestID join의 `sent−recorded`로 드러난다. runtimeFacts는 로컬 requests의 final 없는 assigned만 RestartDrain으로 복원한다. `dispatchedNanos=dispatchedAt−receivedAt`으로 접수/배정을 세고 backend 완료 뒤 `final(incomplete,EntrypointRestarted)`을 쓴다.
firstfloor follow는 인스턴스 복원 배정의 가장 이른 dispatchedAt(없으면 프로세스 시작)부터 읽고 열린 행 없는 줄은 버린다. `RetainedDefinition`은 삭제하지 않는 루트 definitions.jsonl(KB·정의 수 비례)에서 복원하며 같은 identity/본문 재게시를 생략한다.
`SCH-43`/B4: DefinitionRecord→루트 정의→runtimeFacts/NewRuntime/RetainedDefinition과 시간 catalog definitionID를 재사용한다. 삭제·로컬 시간 정리는 루트 정의를 지우지 않는다. Dashboard `resolveWorkloadIdentities`(`7a1d78cb`)는 UID별 현재 등록→관측 definitions→scheduler snapshot→UID 포함 미확인 표시 순이다. 새 동명 UID와 합치지 않으며 요청 모델은 UID 최신 모델 아닌 정확한 definitionID에서 읽는다. archive catalog 정의도 같은 응답에 합치고 RetainedDefinition을 이름 검색 API로 바꾸지 않는다. 삭제/재시작/로컬 삭제·동명 재등록·동일 UID 모델 변경 뒤에도 이 해석을 검사한다.

## 4. 불변 값의 ID 참조
catalog 행은 `{kind,key,at,body}`다. 시간 안 첫 key 참조와 프로세스의 마지막 본문이 바뀔 때 쓴다. 각 시간은 모든 참조를 담고 at은 참조 기록 시각이다. 읽기는 같은 key의 기록 시각 이전 마지막 행(DuckDB ASOF JOIN), 그 이전 기록은 첫 행을 쓴다. 재시작 동일 본문은 결과를 바꾸지 않는다.
| kind/key | 참조 |
|---|---|
| definition/definitionID | 요청, snapshot workloadIDs(workloads 대체), outstanding[].definitionID |
| profile/profileID | 요청, keep/target/options 인스턴스 |
| cost·baseline/`<id>@<reference.key>` | snapshot costRefs/baselineRefs, 측정 evidence object |
| meter/meter ID | 전력; 본문은 node/scope/source/sampling/coverage |
target.id==keep.id이면 `{id,keep:true}`(7.7KB 절약). 전력은 `{at,startedAt,node,meter,samples,failures,powerJoules,powerError}`(at=관측 끝, failures=실패 때만)이며 재계산 가능한 summary는 생략한다. options는 가변 입력이라 유지한다. SchedulerObservationPage는 참조/target 본문을 복원해 기존 API/Dashboard 형태를 유지한다. candidates 요약·무부하 변경 시만 기록은 M4 소관이다.

## 5. 세그먼트와 보존
파일은 `<directory>/v1alpha5/<YYYYMMDDTHH>/<stream>.<MM>.jsonl`(쓰는 시각 UTC 분), 루트 `v1alpha5/definitions.jsonl`은 DefinitionRecord append다. 초기 stream은 requests/scheduler/management/catalog, E5 추가는17장. 경로가 버전이므로 행에 schemaVersion/window는 없다.
1분 archive ticker는 지난 시간의 분 파일을 stream별 순서로 이어 gzip(`compress/gzip`, 헤더 시각0)하고 `serving/observations.v1alpha5/<YYYYMMDDTHH>/<stream>.jsonl.gz`에 PutImmutable한다. 동일 입력은 동일 gzip→Existing; 모든 stream 성공 시간은 프로세스 메모리에 기억한다.
gzip `.archive-*`와 S3 staging `chill-evidence-*`는 PVC 밖 `/tmp` emptyDir512Mi에 흘려 쓴다. 압축 stream-시간 두 벌 이상 공간이 필요하며 PVC full에도 업로드를 시작할 수 있다. 8MiB 비압축성 시간의 할당21.8MB→0.86MB; 기동 때 두 staging 잔재를 지운다. Flush는 serve loop 밖 goroutine 하나, 진행 중 tick 생략, source refresh/Advance는 기다리지 않고 종료는 진행 Flush 뒤 Close의 마지막 archive다.
시간의 모든 stream이 Created/Existing이고 디렉터리 시작부터 retention 경과 때만 삭제한다. `servingEntrypoint.observationStorage.retention`→`--observation-retention`→`ObservationLogConfig.Retention`은 양수 Go duration, 기본2h(현재/직전 시간), 업로드1분이다. 하나라도 실패하면 해당/이후 시간 유지·다음 pass 처음부터 재업로드; 미완료 assigned 시간/이후도 pin한다. mu 아래 삭제하므로 조회는 시간 전체를 열거나 삭제된 것으로 본다. S3 중단은 PVC 누적→append 실패→readyz503이다.
append 실패는 쓰기 전 크기로 rollback(루트 정의 포함)하고 final은 열린 행에 남겨 다음 Flush 재시도한다. 기동은 stream 마지막 분과 루트 정의를 마지막 개행까지 자르고 루트 정의/로컬 requests만 읽는다. 완료 row는 nil tombstone으로 payload를 버리되 늦은 assigned를 막고 native는 무시·마지막 assigned만 복원한다. ID map은 전체 요청 수, payload는 미완료 수; torn-tail은 끝에서4KiB씩 읽는다. 읽기는 mu 아래 fd 확보 뒤 잠금 밖에서 진행하며 삭제는 열린 fd에 영향 없고 미완결 마지막 줄은 무시한다.
D2(`SCH-42`): 재생 가능한 동안 evidence store 보존, 로컬 삭제는 영구 삭제가 아니다. `7ad977d1` retention(기본2h/시연24h)은 임시 버퍼 조절이지 과거 조회 해결이 아니며 기간 연장만으로 T4 완료가 아니다.
### 5.1 E5 전 운영 보존·기동 예산 결정 (2026-10-27)
values-operating.yaml의 임시24h(`e659ccb1`, 시연2회차)를 기본2h로 복원한다. 공통 prepare/selectSources는 없는 로컬 시간을 S3로 읽고 UTC hour 수정 `81ea4c59`,`6b2755ec` 반영; `TestObservationArchiveTransparentReadsAndCatalog`는 로컬 삭제 뒤 Query/LoadWindow/scheduler 동일 원자료·정의를 검사한다(이번 조사 재실행 없음).
E5 archive collect는 drain+1분 지원 표본 뒤 retained fd를 먼저 열고, 없는 과거 시간은 exact-hour S3 List→fetch→gzip EOF/CRC로 수집한다. 현재 시간 로컬 필수, required scheduler 부재/S3 실패는 NOT_READY다. 기존 raw/power reducer는 같은 obs를 읽어180분 E5·170분 static도 의미가 유지된다. 정상 S3 접근/보존·collection deadline 내 다운로드가 전제이며 E5-0 성공을350분 수집 시간 검증으로 대체하지 않는다. 6h/24h는 정확성에 불필요하다.
`34ec34f7` 이후 restore는 순차 decode, 완료 payload 폐기/완료 ID nil tombstone 유지다. 비용=`definitions bytes+Σ로컬 requests bytes` I/O·decode + O(전체 고유 ID) map/O(미완료 payload). scanner64KiB/최대 한 줄, tail4KiB; scheduler/management/catalog 전체를 constructor에서 읽지 않지만 catalog/Kubernetes 후속 초기화는 별도다. retention은 기동 후 삭제 조건이라 최초2h rollout도 기존24h를 먼저 복원하며 수동 삭제/빈 디렉터리 전환은 하지 않는다.
2026-10-27T10:03:32Z read-only stat, Pod UID `e8dc048a-91f6-4be9-851c-7c95ac2a8933`: 24h 실제24시간/2,022파일·전체14.251GB·requests11.123GB; 6h 가상6/428·0.927/0.430GB; 2h 가상2/74·0.152/0.00937GB. 루트 정의416,375B 별도. 가상값은 업로드 성공/pin 없음의 삭제 후 추정이다. requests 50MB/s 가정222.45/8.60/0.19s는 `[INFERENCE]`이며 ETA가 아니다.
rev93 실제 로그는147.043s `456→459→456MiB`,153.159s `880→880→15MiB`, Ready약294s로 순수 restore/향후 상한이 아니다. 재시작 archived map은 비어 첫 pass에 지난 모든 stream 재gzip/PutImmutable한다. 위 자료는 지난 원문14.241GB 추가, constructor 합25.363GB 읽기이며 staging/S3는 별도다.
150MB gzip/h 사용자 가정(MB=10⁶B,GiB=2³⁰B)과 manifest `preflight-control-195-u2x/smoke-MIX`의 gz32,869,396B/원문158,205,468B=혼합4.813배(requests12.673배) 조건부 `[INFERENCE]`: E5 180분450MB/원문2.166GB, static170분425MB/2.046GB, 합350분875MB(834.5MiB)/4.212GB; 2h≤300MB/1.444GB,6h≤900MB/4.332GB,24h≤3,600MB/17.327GB(16.14GiB). ≤는 일정률·정상 업로드·분별 삭제·pin 없음 버킷 근사이지 상한이 아니다. 2h는 앞 시간을 S3로 옮겨350분 전량 누적하지 않는다.
모두 requests12.673배라면24h45.624GB로32Gi PVC 초과다. 현재24h13.27GiB/gzip3.6GB는 안전 인증이 아니다. 최대 stream-시간 gzip150MB라면 scratch두벌300MB=286.1MiB지만268.4MB 초과면512Mi 초과다.
GOMEMLIMIT3072MiB/limit4Gi 유지(rev96 확인). 관측 heap최대880MiB와 soft/hard 차2,192/3,216MiB는 복원 기동 여유이지 RSS/charged peak·전체 E5 메모리 인증이 아니다. target-limit1,024MiB는 runtime/stack/cache/scratch 보장이 아니며 요청 body/queue·ID map·미완료 payload·catalog·긴 조회는 별도다. 150MB/h·κ로 요청 수/heap 상한을 환산하지 않는다. 증설 없이2h로 줄이고 CampaignOps 직렬 적용/E5-0의 cgroup/GC·Ready/restart·archive 출처/공백을 보존한다.
근거: `O=~/.cache/chill-assets/ops/pools-deploy`, `O/intent/observation-retention-20261027.json`, `O/campaign/v2/d-20261027/control-192/{startup.log,gc-heap.json}`, `O/deployB/control-195-20261027/pods-ready.stdout`. 라이브 적용은 CampaignOps 소유이며 이 조사에서 부하·빌드·측정 중 재시작을 하지 않았고 설정 커밋은 적용/검증 완료가 아니다.

## 6. 조회
### 6.1 범위와 기존 reducer
| 조회 | 범위·모집단 |
|---|---|
| Query(`/observations/workloads/{uid}`) | requests는 from 분~최신(늦은 final 포함), management는[from,to], 열린 행. 모집단은[from,to)에 접수된 요청 |
| LoadWindow | requests from 분~최신+열린 행; receivedAt+오프셋이 구간 안인 단계 수, 처리 중도 도착 포함 |
| OperatingPower | scheduler[from−1분,to+1분], 기준 snapshot은 from부터 거꾸로 첫 snapshot 분까지; maximumBoundaryDistance≤1분, meter catalog coverage 사용 |
| SchedulerObservationPage | scheduler[from,to], 기존 limit에서 종료 |
파일 이름을 색인으로 envelope 먼저 decode하고 필요한 본문만 읽으며 참조는 해당 시간 catalog로 푼다. archive 이후 실제 없는 범위만 gapUntil/coverage gaps이고 power complete=false를 유지한다. SchedulerHistoryWindow는 현재 없고 도착 경로는 LoadWindow의 Arrivals다. Tick60s는1~2분 파일·5req/s<1MB(이전3.3~6.7MB); 메모리는 선택 요청 수 비례, 기본2h/5req/s는36k행≈47MB다.
### 6.2 투명한 로컬·archive 읽기 (`SCH-42`)
ObservationArchive=기존 evidencestore.Reader+Lister와 CapacityRecords의 readArchivedStream gzip scanner를 재사용한다. 같은 v1alpha5 prefix/네 stream, cmd/chill-server의 objects를 쓰며 새 저장 형식·TSDB·옛 reader는 없다.
capture가 mu 아래 로컬 시간/fd/크기를 확보→잠금 밖 List로 필요한 UTC 시간 선택→오름차순 scan한다. **시간 디렉터리 단위 로컬 우선**, 없는 시간만 archive+그 catalog를 읽고 로컬 stream 부재를 archive로 보충하지 않는다. fd 확보 뒤 삭제도 중복/누락을 만들지 않는다. requestID final>open>assigned로 archive assigned/늦은 로컬 final을 병합하고 scheduler/management/power는 같은 시간을 두 소스로 더하지 않는다. page는 합친 시간순 append에 offset/limit, errObservationStop은 archive에서도 정상 종료다.
기존 본문/필터/nextCursor에 `coverage:{from,to,sources:[{from,to,stream,source}],gaps:[{from,to,stream,reason}]}` 추가: RFC3339Nano, source=local|archive(archive는store/key), from/to는 확보 외곽·전무하면null, 내부 gap은gaps, gapUntil은 실제 선두 공백만이다.
빈 stream은 archive 객체를 안 쓰므로 필요한 시간/stream 부재는 StreamUnavailable이며 0건 단정/새 manifest는 없다. catalog 누락·gzip/JSON 오류·List/Open 실패는 조회 오류다. power 경계/기준 snapshot 부재는 기존 complete=false/이유, 읽기 전용 오류는 쓰기 readyz를 내리지 않는다.
Query/LoadWindow/SchedulerObservationPage/OperatingPower/ScanOccupancyRows는 첫 인자 context.Context를 받고 HTTP RequestTimeout/취소 또는 controller capture ctx를 List/Open/scan까지 전달한다. 네트워크는 mu 밖, 기존 limit 유지, 최근 창이 로컬에 모두 있으면 archive 호출은 없다. workload는 정의 병합 후 aggregate/page, load는 단계별 집계, scheduler full/compact는 catalog 복원+coverage, power는 더 오래된 archive 기준 snapshot/catalog까지 역탐색하고 기존 ReduceOperatingPower를 쓴다.
검사 경계: 로컬/archive 동시 존재·삭제 전후 동일 수/단계/page/J와 출처, late-final 1회 집계, 페이지·삭제 UID·power 앞뒤 표본/오래된 snapshot·내부 누락·store 오류/취소/gzip 종료·조회 중 삭제다.
### 6.3 측정 CR 두 종류의 읽기 projection (API B1, `SCH-44`, `DB-FR-45`)
GET `/api/v1/namespaces/{namespace}/operation-cost-measurements[/{name}]`, `.../dispatch-occupancy-measurements[/{name}]`: 목록 `{items:[projection]}` 이름순/빈[], 상세 projection/없으면404. whitelist=`{kind,metadata:{namespace,name,uid,resourceVersion,creationTimestamp},node,conditions,status,references}`. cost status=`{phase,reason,steps:[{name,key,verdict}]}`, occupancy=`{phase,reason,powerMode,perRequestNanos,throughput,result}`; 실패/미완료/결과 없음은 그대로,0 대체 없음.
conditions: occupancy `{workload,batchEnergyOptimization,batch,supplyMode,warmupSeconds,durationSeconds,workers}`, cost `{steps:[{name,kind,powerMode?,batchEnergyOptimization?,idleDurationSeconds?}]}`만 원 spec에서 복사한다. 참조는 CR namespace 안, 미지정은 미지정이며 inputsJSON/전체spec/raw는 비노출이다. references=`{cr:{apiVersion,kind,namespace,name,uid},evidence:[{store,key}]}`; 실제 status.steps[].key/result만 사용, 결과 없으면[], 추측 key/임의 URL/호스트 경로 없음. UID로 재생성을 구분하고 기존 객체 권한을 따른다.
기존 measurements bearer/namespace/name 검증·AuthorizeResource와 measurement service List/Get 패턴을 재사용한다. 두 CR 각각 caller namespace list/get SAR(서버 SA 대체 금지), 쓰기 추가 없음. 검사에는 원본 일치/조건 whitelist·신원/key·list-only/get-only·권한 거절·404·Measuring/Failed/Insufficient·비노출을 포함한다.
### 6.4 실험 불변 요약 조회 (API B2, `SCH-45`)
GET `/api/v1/experiments`는 `{items:[summary]}`(from내림차순/id순), `/{id}`는 summary다. `{schemaVersion:"serving-experiment.v1alpha1",id,from,to,status,stages:[{id,from,to,kpis}],sources:[{store,key}]}`로 [from,to) 단계와 기존 요청/결과별 수·성공/SLO 분자/분모·분위수·전체/활성 POOL J 및 모집단 설명을 보존한다. 미측정은null/이유, 실패/PRIVATE_OPERATIONAL_ONLY/한계를 통과로 바꾸지 않는다.
E5 summarize의 실제 phase 경계/KPI/출처를 `serving/experiments.v1alpha1/{id}/summary.json`에 PutImmutable한다. 원자료 먼저·최종 요약 마지막1회, 정정은 새ID다. 서버는 기존 objects Reader/Lister로 읽기만 하며 실행/재계산·새DB·캠페인 API가 없다. 안전한 단일 id·prefix 제한·non-resource GET authorize/HTTP ctx를 따른다.
`go run ./hack/e5/publish --config <private-store.json> --manifest <private-manifest.json>`: config=`logicalStore,bucket,endpoint,region,accessKeyID,secretAccessKey`(선택sessionToken,tempDir), 공개 응답 금지. manifest `{store,key,localPath}`는 같은 실험 prefix 원자료/summary만 허용한다. 원자료도 Created만 허용하여 부분 중단 후 Existing이면 즉시 충돌/새ID이며 digest/내용 비교로 동일 실행을 추정하지 않는다. 요약 없는 실행은 완료 목록 제외; 목록/상세 일치·덮어쓰기·404/store 실패·권한/이탈·실패 보존이 검사 경계다.
### 6.5 축약 상태 조회 (API B3, `SCH-46`)
GET `/api/v1/scheduler/state?view=summary`=`{at,nodes,instances,routing,decision:{id,at,status,reasons,targetID},actions}`. 노드 식별/전력모드/ready/measurement/reasons, 인스턴스 식별/node/소유·배분UID/profileID/ready/routable/이유, planned/running action ID/종류/상태/의존/elapsedNanos/예상 시간만 포함한다. decision.id는 기존 판단 UTC RFC3339Nano, routable은 UID별 서버readback(T2)이며 UI 추론 없음.
scheduler_summary.go 독립 whitelist DTO를 같은 controller 상태에서 투영한다. 원 struct embed/전체JSON 생성 후 삭제는 안 하며 profile/κ/options/costs/후보/과거results/raw는 제외한다. summary는/state만 허용·BFF 기본compact/full 의미와 권한 유지. 검사: 원본 ID/노드/배분/action 일치, 큰 options에 크기 비례 없음, 잘못/중복view 거절·비노출이다.
### 6.6 작업 예상 시간 (API B5, `SCH-47`)
action에 `expectedSeconds:number|null,expectedSource:{costID,reference,statistic:"mean",trialCount}|null`. 선택된 동일 조건 cost의 유효 실측 trial durationNanos 산술평균 초값이며 route/stop/drain 등 근거 없으면 둘 다null(상수/0 금지). action 실행 예상이지 DAG ETA/timeout/elapsed가 아니고 startedAt/completedAt/elapsedNanos는 별도다.
selectCosts가 고른 CostID/Trials/Reference로 capacityOperationCost의 평균 규칙을 재사용해 한 번 산출하고 실행 중 근거를 고정한다. kube actions/scheduledOperation/Operation→recordAction→SchedulerActionObservation, 수동작업 동일 조건, full/compact/summary/저장/live 모두 같은 값이다. 과거 필드 부재는null, 과거 cost 재대입 없음. 조건/평균/출처/개수·근거 없음·실행 중 갱신 불변·실측 분리가 검사 경계다.

## 7. 측정 표 산출
과거 오프라인 경로의 원자료는 S3 시간 catalog/streams와 loadgen JSONL이다. `hack/observations/fetch.sh <from시> <to시> <dir>`는 obs 시간 prefix와 power용 앞뒤1시간을 받고 단계 loadgen은 `<dir>/loadgen/<단계>.jsonl`에 둔다. `e1.sql`은 요청/포화/Tick(evaluationElapsedNanos) 표, 완료 여유는 [계획기](analytic-planner.md)다. `power/main.go --dir <dir>/obs --scheduler-config <file> --from --to`는 전체/활성 POOL J이며 수락된 meters와[from−1분,to+1분] segment가 필수다. E5 새 수집은17장 API만 쓴다.
요청은 loadgen LEFT JOIN final(requestID), 분모=sent, sent−recorded=서버 행 없음; p95는 gonum Empirical, completed/e2e<1s, 잔여p50/p99/max는 유효 표본이며 executionComplete일 때만 계산한다. SLO는 catalog definitionID join이다. 전력 적분/snapshot 소속은 서버 OperatingPower reducer iterator를 재사용하고 catalog meter는 본문 해석용일 뿐 선언 집합이 아니다. 구조/필드 검사만 두며 digest/재인코딩/byte 비교 관문은 없다.
## 8. 예상 크기
M1 당시 `[추론]` 원문/gzip: 2req/s는 실측273MiB/h→요청9.3+판단46.9+전력6.3+catalog0.2≈63MB/h·gzip5.5MB/h; 5req/s는이전약582MB/h→23.2+46.9+6.3+0.2≈77MB/h·gzip7.4MB/h; 무부하는실측45MB/h→11.6+6.3+0.2≈18MB/h·gzip1.9MB/h.
산출: 요청1.29KB×7,200/18,000; 판단185−target7.7−중복51+참조4≈130KB×360=46.9MB, 무부하87−7.7−47=32KB×360=11.6MB; 전력763B+식별110B≈0.87KB×7,200=6.3MB; catalog 비용/baseline40KB+정의수KB+profile16KB×사용 수. gzip은 final138B/assigned40B와 scheduler 실측12.6배/무부하9.5배이며 중복 제거 후 비율은 달라질 수 있다.
당시 추정 PVC2h/5req/s≈155MB·32Gi 중단약17일, S3하루180MB/32Gi약180일, 임시24h≈1.86GB(+1.705GB/5.4%)는 실측 보장이 아니다. 현행 운영 예산·기동 비용/2h 결정은5.1절이 우선한다.
## 9. 계약과 버전
M1은 `v1alpha4→v1alpha5` 단일 cutover([ADR 0019](../../adr/0019-durable-stored-contract-compatibility.md)) 계약이었다. 당시 절차는 루트 정의를 이행하지 않아 AppliedSnapshotUnavailable이 된 Workload를 동일 spec으로 재적용하고 Completed/Ready·binding/routing 복구를 확인하도록 했다(실제 수행 증거가 아님). 옛 창 reader/디렉터리/S3 `serving/<windowID>/` 삭제는 과거 절차이며 E5에서 재실행하지 않는다.
현행 Query.requests는final/없으면assigned; incompleteWindows→gapUntil, ObservationLogHealth.windows/observation-window flag·Helm observationWindow는 제거, ObservationGap과 maximumBoundaryDistance≤1분을 쓴다.
## 10. 삭제
창 metadata/census·전체 사건 재조립·definitions stream은 시간 파일/범위 reader·루트 정의/catalog로 대체됐다. 무한 이력을 빠르게 읽는 옛 장치/검사/옵션을 새 경로로 복구하지 않는다.
## 11. 커밋 단위
과거 M1 작업 순서는 생략한다. 현행 저장·조회 계약은3~6장, E5 코드/배포 근거는17.7~17.8절이다.
## 12. 검사
계약 검사:1분 상한·분 선택/이어진 gzip·부분 archive 실패/Existing 재시도·retention/pin·torn-tail/rollback·범위 읽기/삭제 경쟁, 요청 cohort/늦은final/중복/restart/배정rollback, catalog 시간별 완결/변경/재기록·동일J, T4 인증/projection/coverage다. 당시 목표는 요청≤1.5KB(19참조), 판단≤135KB; offline power=서버J다. M1 검사 통과를 T4 통과로 재사용하지 않는다.
## 13. E 확인
당시 E1 2req/s1h+무부하1h 목표: PVC2h원문≈130MB, S3≈6/2MB/h, 요청≤1.5KB, 판단중앙≤135KB, 무부하≤20MB/h, 2h PVC 재시작Ready시간(이전27~31s)·요청별final1개/RestartDrain종료 기록, 2분/1h/2h조회 OOM0, API/offline 수·p95·잔여·POOL J 일치, crash없으면sent−recorded=0이다. 목표는 실측 통과 주장이 아니다.
## 14. 사용자 결정과 질문
M1에서 Prometheus류 제외, 배정 전 crash손실 수락, RestartDrain/assigned약300B 유지, stdlib gzip, 기본2h/evidence store 영구조회가 결정이다. 배포 전 testbed ConfigMap maximumBoundaryDistance≤1분 확인은 남은 운영 조건이며 이 문서가 클러스터를 읽었다는 뜻은 아니다.

## 15. U5: 직접 native 진행 기록 결손 진단 (`SCH-31`, `SCH-42`)
관측 완전성과 `00_measurement.md`의 native 누락=Insufficient를 유지하며 새 합격조건·stamp추정·진행재시도는 없다. ProgressLoss27/ContinuityReplay27의 D05(batch92866) running·D10(batch63056) queued 직접 stamp는 requests archive에도 없었다. 닫힌 요청 supplement/finished 선도착 처리로는 생성/stdout·CRI/follow·parse/지속화 중 소실 단계를 확정할 수 없다.
### 15.1 실패 진단
emitProgress는 marshal/4096byte초과/stdout쓰기 오류에 requestID·phase·오류(실제bytes/limit)를 남기되 정상 progress는 복제하지 않는다. ParseProgress는 거절 시 decode된 식별자를 진단용 반환; Observer.scanLogs는 parse/ObserveBackend runtime·context/late-supplement 실패를 native source issue에 requestID/phase/physicalID=PodUID/containerID로 남기고 오류 반환값을 runtime에 전달하지 않는다. decode불가 식별자는빈문자열이다.
native에는 PodUID/containerID 입력이 없어 hostname으로 꾸미지 않고 exact manifest와 join한다. observer는 실물 identity를 직접 기록하며 issue가 다음 정상 행에 지워지므로 빈/pools issue는 과거 오류 없음이 아니다. 서버 원문 보존이 필요하다. observer는 서버 재빌드, emitter는 새 runtime 이미지가 필요하지만 runtime7 digest동결을 깨지 않는다. 이 변경만으로 D05/D10 원인/3회 무재발을 주장하지 않는다.
### 15.2 원본 보존과 판정
occupancy는 load Job Complete/Failed 직후 deleteLoadJobs→drain/집계/보존이므로 CR최종상태나 pods-return 뒤 logs는 늦다. finalizer는 stdout archive 보장이 아니다. 운영자는 다음 CR 생성 전 `hack/measure/capture-progress.py --node ... --measurement ... --namespace ... --output-dir ... --capture-load --timeout 1800`를 시작해 firstfloor timestamp-follow와 exact CR UID hashed load Job/Pod를 외부 보존한다. 종료 직후 snapshot도 시도하되 삭제/실패/identity불확실은 완전보존으로 표시하지 않는다.
READY는 최초 Pod조회·알려진 컨테이너 follow 시작/생존 확인 뒤만 출력한다(미생성/종료는실패). READY 뒤 끊김은 보장하지 않으므로 stderr/종료코드/범위를 확인하고 이후 같은 이름 CR을 생성한다. 기본KUBECONFIG/선택--kubectl, 산출물 manifest.json/follow.log/follow.stderr/snapshot.log와 서버로그를 사용한다. 겹친 follow/snapshot은 합쳐 이중계산하지 않는다. Pod JSON(UID/containerID/imageID/restartCount/Node), CR/Job신원은 private 외부 run에 보존하며 git/public복제 금지다.
누락ID/phase를 exact원문/archive/observer오류로 대조한다. 완전한 원문 행은 emission 이후, malformed/partial·write native progress는 출력/framing 증거다. 원문 없으면 회전/삭제/follow실패/시간공백부터 확인하고 --previous로 전부 복구된다고 하지 않는다. 필요한 CRI/회전 원문은 운영자가 읽기 전용 회수한다. 파일 부재는 미출력 증명이 아니다. direct stamp가 확보된 실물만 재생하고 legacy offset/QueueMS합성 금지; 회수불가는Insufficient/재측정이며 runtime닫힘/finished·occupancy판정을 완화하지 않는다. 진단 회귀/offline self-check와 실제 원인/3회무재발은 별도 근거다.

## 16. U6: 부하 발행 공백과 trace 충실도
실험도구 변경으로 새spec/SRS는 만들지 않고 SCH-31 모집단/재계산·SCH-45 불변요약에 연결한다. 발행충실도는 분모를 바꾸거나 미발행을 성공으로 만들지 않는다.
### 16.1 보존 원시의 관측 사실
`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`; batch1-plc-3 raw.loadJob.name을 공통 `O/campaign/v2/loadpods/<Job>-<Pod suffix>.jsonl`에 join했다. 완료8개 전체421.010초(워밍업/tail포함, 물리360초창 아님)의 scheduledOffsetNs 인접차는 submit직전 표식이며 socket/stdout시각이 아니다. 분위수는 zero-based floor(N×p), E5 stats는 nearest-rank다.
전체 961,435gap 중>50ms 3,541개. 원시별 `요청수/>50ms수(%)/p50,p95,p99,max ms`: D05 197757/99(.0501)/1.421,4.714,23.652,772.353; D11 99368/1130(1.1372)/1.444,7.957,50.773,525.441; D10 120094/141(.1174)/2.294,8.653,31.156,450.794; D15 147933/67(.0453)/2.271,6.018,15.889,959.400; D18 123642/74(.0599)/2.251,5.474,35.628,873.821; D21 53912/103(.1911)/5.586,32.679,39.309,313.049; D26 137308/1839(1.3393)/1.443,5.008,51.443,657.648; D25 81429/88(.1081)/5.095,5.806,8.221,551.913.
scheduledOffsetNs+latencyMs의 추정 완료간격도 길다(정확시각 아님). D11최대offset388431281812→388956723072ns, D26 146217337659→146874985548ns는 각각 시작미완료약64·구간추정완료1개로 관측 client완료 경계의 closed-loop공급 부족이지 producer lock원인 확정이 아니다.
gctrace/cgmon의 window.start−60s UTC대응은근사다. D11 GCclock `.085+4.6+.003`,`.12+4.2+.016ms`,CPU1.49→.29core/throttle0; D26 `.17+18+.014`,`.10+4.9+.031`,`.079+3.3+.002ms`,CPU1.04→.49/throttle0. 525–658ms STW/gateway throttle 증거가 아니며 concurrent항을 STW에 합치거나 다른 지연을 배제하지 않는다.
S3 `serving/observations.v1alpha5/20261026T19/requests.jsonl.gz`를 read-only `.../d-20261027/raw-19/`에 보존(143,588,423B,SHA256 `97a16157e0e705ff48adfcf01eed4a9e37b9c0298aea1ebaadf03d3e38739846`,원격쓰기0). D11 99,368개 전부 exact final join. `exchangeNanos−dispatchedNanos−clientSpanNanos`(assignment대기/직렬화/스케줄/응답decode잔여,순수mutex아님) 전체p50/p95/p99/max=.198/.633/.932/445.510ms,>50ms164개; 최대공백미완료64코호트p50/p95/max=.239/.512/.588ms,>50ms0. 코호트완료시각만client추정,잔여는서버단조원시다.
결론: 최대공백을 ObservationLog assignment writer체류 급증에 귀속할 근거가 없다. native HTTP/전달/실행/client스케줄·응답 중 원인은미확정 `[INFERENCE]`; loaderCPU/throttle·lock/flush·exactUTC/monotonic anchor가 없다. 다른창pprof경합은 인과증거가 아니다. 상세 `.../d-20261027/u6-issuance-gaps.json`, `raw-19/{receipt.json,u6-gateway-residual.json,analyze-u6.py}`는private이며 공개복제하지 않는다.
### 16.2 최소 구현과 E5 결과
CLI replay/주입E5 producer는 JSON직렬화만 stdout공유lock 밖으로 옮기고 완전한 한줄 write+flush는lock으로 보호한다. worker는flush뒤 다음요청, writer thread/queue는추가하지 않는다. 이는 불필요critical-section축소이지 공백원인/해결증명이 아니다. CLI의 sentMonotonicNS/finishedMonotonicNS는 infer직전/응답·transport오류처리직후 같은프로세스 단조시각이며 stdout/패킷/gateway수신이 아니다.
issuanceFidelity는 실제producer start/send로 발행순 정렬하여 시작/끝포함gap p50/p95/p99/max·>50ms개수/비율/구간offset/안의planned수, frozen trace planned-gap분포·send lateness분포/>50ms수를 judgment/e5-summary/출판KPI에 보존한다. Poisson/0부하 자연공백을 자동실패로 하지 않고 timestamp없음UNKNOWN/NotReady·offered0 NOT_OFFERED, 미발행/결손0대체금지다. 기존10ms delayed와50ms보고는 별개다. demo E5-0~7도 d2/analyze_before.py 공통계산을 e5 judge/demo summarize·단계/전체summary.json/md·publish workload KPI에 쓰고 결손은UNKNOWN/이유다.
다음 측정은15절 load로그, E5는producer start/monotonic anchor·client/server/edgemesh cgroup·GC를 보존한다. CLI적용은 load-generator새이미지/digest/receipt가 필요하며 주입파일 변경만으로 CLI배포를 주장하지 않는다. U6는runtime7재빌드가 불필요하다.
### 16.3 별도 writer 개선의 설계 경계(미구현)
위 근거로 U6에서 observation_log.go는 바꾸지 않는다. 별도 배정 시 immutable snapshot/closing을 requestMu아래 확보→밖에서marshal→writer mutex아래 closed/현재row·참조재확인→catalog/request를같은commit-time hour에 append한다. JSON완료순 아닌기존append순, assigned성공전 native시작금지다. final실패는closing유지/Flush재시도, phase갱신시pointer비교로최신row삭제금지다. Runtime/Flush경쟁·close/hour/catalog-first를보존하며 Flush의기존mutex경로를helper임시unlock만으로고치지 않는다. 모든caller와diagnostic mark를함께이행하고 약50줄초과/순서변경은별도설계·회귀검사다. 비동기장부/generic queue는없다.

## 17. E5 증거 API (2026-10-30)

[계획 §1~6](../../plans/2026-10-28-e5-evidence-api.md)의 구현 계약이다. 원본 요구는 [Serving 관측](../../spec/00_measurement.md#측정의-단위), [scheduler SRS](../../spec/scheduler/SRS.md)의 SCH-67~70 / SCH-AC-52~55다. 코드 이행과 실제 배포·실험 수용은 구분하며 구현 커밋은 17.7에 남긴다. 기존 `ObservationLog`, `ObservationArchive`/Reader/Lister, 로컬 우선 시간 선택, `append`/archive/health, TokenReview/SAR, live hub를 재사용한다. 별도 저장소·TSDB·broker·다중 버전 reader·router 파라미터를 만들지 않는다.

### 17.1 범위와 수집 경계

하네스가 아는 CHILL 내부 사실은 CHILL API에서만 얻는다. CHILL 대상 sudo/crictl, `/proc` 직접 읽기, SSH cgroup 수집, S3 다운로드, kubectl readback, observer 로그 복사를 cutover 때 제거한다. `raw-source-index.json`은 판정 입력으로 유지하며 API 수집기가 만든다. 원시 전송·과거 조회는 측정 대상 서버의 CPU/I/O를 쓰므로 **E5 측정 창 밖**에서 실행한다. 라이브 보기만 창 안에서 허용한다(운영 규칙, 앱의 새 강제 관문은 없음).

| 사실 | 단일 출처와 제외 경계 |
|---|---|
| 요청·판단·정의·관리 원시 | 아래 raw API. 대시보드용 `/observations/workloads`, `/scheduler` 가공 조회는 유지 |
| CHILL 컨테이너 CPU/throttle/메모리·thermal | `resources`. `client` 컨테이너 표본·시작 관문·생산자 해제는 외부 장비라 유지 |
| GC·관측 디렉터리 바이트 | 서버 in-process `resources`. gctrace 강제 요구·pre/post PVC host 수집 제거 |
| Pod 신원/상태·제어 image digest·observer 오류 | `cluster`. E5-7 사후 readback은 `scheduler`와 `cluster`; 운영자 파일에는 장애 주입 명령만 남김 |
| Node 참조·POOL Node 목록 | 기존 state/scheduler의 Node 이름·UID. 합성 NodeList나 새 Node/cordon stream 없음 |
| 전력계↔Node 선언 | settings `meters`; 전력 정책은 기존 `powerObservation`. 실제 전력은 기존 edge-metrics API 유지 |
| 수집 영수증 | capture-identity, s3-hour-list-*, upload.log, collector-status, *-collector.stderr와 이를 읽는 판정 코드 삭제 |
| 운영 안전 | `control-alerts` STOP 루프, 생성기 품질 관문, Job 조작과 E5-7 운영자 kubectl 명령은 그대로 유지 |

판정 수식과 기존 publication은 바꾸지 않는다. GC는 사이클 분포가 아니라 창 안 누적 차, E5-7 recovery는 운영자 readback이 아니라 scheduler/cluster 출처임을 새 보고서에 명시한다. 다음 새 publication의 `measurementRules`에 `evidenceSource=api`, `gcMeaning=runtime-metrics-delta`를 기록한다. #213~#225를 수정·재게시하거나 비교 가능성 판정 관문을 추가하지 않는다.

### 17.2 원시 내보내기 (`SCH-67`)

`hour`는 UTC의 안전한 단일 경로 요소 `YYYYMMDDTHH`다. 누락·중복 hour, 잘못된 시각, 허용되지 않은 kind, 추가 경로/쿼리·경로 이탈은 400으로 거절한다. 파일명·store/key를 요청 입력으로 받지 않는다. kind는 `requests`, `scheduler`, `catalog`, `management`, `resources`, `cluster` 여섯 개뿐이다.

| 요청 | 응답 계약 |
|---|---|
| `GET /api/v1/observations/raw?hour=<hour>` | JSON `{hour,at,status,kinds:[{kind,available,segments:[{minute,bytes}],archive:null 또는 {store,key,bytes}}]}`. `at`은 목록을 잡은 서버 시각, `minute`는 `00`~`59`. 로컬 segments는 닫힌 분만, 분/종류 오름차순이다. archive 출처면 segments는 빈 배열이며 저장된 gzip 객체 위치·크기를 싣는다. 토큰·호스트 경로·자격증명 URL은 노출하지 않는다 |
| `GET /api/v1/observations/raw/{hour}/{kind}` | 로컬 시간은 요청 시작 시 확정한 닫힌 분 파일을 분 순으로 이어 원래 JSONL 바이트를 전송(`application/x-ndjson`). 로컬에 없는 과거 시간은 해당 stream의 보관된 gzip 바이트를 그대로 전송(`application/gzip`). gzip에는 `Content-Encoding: gzip`을 붙이지 않아 HTTP client가 자동 해제하지 않게 한다. 둘 다 `Cache-Control: no-store`; 즉석 gzip/해제·JSON decode/re-encode·필터·집계 없음 |
| `GET /api/v1/observations/raw/definitions` | 루트 `v1alpha5/definitions.jsonl`을 요청 시작 시 확정한 마지막 개행까지 원래 JSONL로 전송. 마지막 미완결 행 제외. 시간별 catalog의 대체물이 아니며 빈 완결 기록은 길이 0 |

시간 상태는 다음처럼 source와 진행을 구분한다. `open`은 현재 UTC 시간으로 닫힌 분이 일부 있어도 open이다. `closed-local`은 지난 시간이고 로컬 디렉터리가 남은 상태다. `archived`는 로컬이 없고 기존 prefix에 보관본이 있는 시간이다. 업로드됐어도 로컬이 남으면 로컬이 우선이다. 기본 보존 `2h`와 archive 성공 전 삭제 금지·미완료 assigned pin을 그대로 지키므로 경과 2시간만으로 archive 사용을 강제하지 않는다.

- 로컬 선택은 기존 시간 디렉터리 단위 우선순위다. 파일이 없는 stream만 archive에서 보충하지 않는다. 같은 시간을 두 소스에서 합치지 않는다.
- `mu` 아래에서 시간·현재 분 경계·선택한 fd·길이를 잡고 잠금 밖에서 한정된 길이만 복사한다. 원본 fd가 열렸으면 archive 삭제와 충돌하지 않는다. 현재 열린 분은 파일 크기나 개행 유무와 관계없이 제외한다. 목록과 다운로드는 각각 독립 snapshot이며 목록 시각을 고정하는 서버 영속 receipt/manifest나 재시도 세션은 만들지 않는다.
- `definitions`도 mutex 아래에서 fd와 마지막 완결 행 끝을 확보하고 이후 append를 전송에 섞지 않는다. 기존 시작 시 torn-tail 복구와 append rollback을 유지한다.
- 시간 자체가 양쪽에 없거나 선택한 stream이 없으면 404(`ObservationUnavailable`), 현재 시간에 아직 내보낼 닫힌 분이 없으면 409(`ObservationNotReady`)다. 목록의 kind별 `available=false`는 부재이지 0건의 관측을 뜻하지 않는다. 열린 시간의 빈 segments도 수집 완료가 아니다.
- 새 stream은 [ADR 0030](../../adr/0030-observation-evidence-api-streams.md)대로 같은 prefix에 추가한다. 배포 이전 resources/cluster 부재는 결손이 아니며 #213의 기존 네 stream 조회를 막지 않는다. 적용 이후 필요한 stream의 부재는 소비자가 수집 적용 구간·실제 표본 시각으로 결손을 드러낸다. 새 stream 여섯 개의 존재를 모든 시간의 필수 조건으로 삼지 않는다.
- archive List/Open 오류는 5xx, 처리 마감은 기존 timeout 오류다. 읽기 오류를 빈 200/404로 바꾸지 않는다. 전송 시작 뒤 실패는 응답을 중단하며 JSON 오류를 JSONL/gzip 뒤에 덧붙이지 않는다. 알려진 길이는 Content-Length로 제공해 조기 종료가 드러나게 한다. 하네스는 HTTP 완료·JSONL 개행/구조·gzip 종료 오류를 검사하고 불완전 수집을 성공으로 판정하지 않는다. digest/바이트 대조 관문은 만들지 않는다.

**마감:** `httpapi.Handler.ServeHTTP`의 `longOperation` 선택을 확장하여 raw 목록/전송/definitions 및 가공 `GET /api/v1/observations/scheduler`에 기존 `OperationTimeout`을 처리 상한으로 적용한다. 별도 무제한 timeout이나 원시 전용 서버를 만들지 않는다. Request context의 처리 마감·취소는 로컬 복사와 archive List/Open/read까지 전달한다. 쓰기 마감은 기존 방식 `처리 deadline + RequestTimeout`으로 별도 설정한다. 추가 쓰기 시간은 오류 전달 여유이며 처리/read context를 연장하지 않는다. archive 네트워크와 대용량 복사는 ObservationLog mutex 밖이다. 기존 가공 조회의 catalog 해석·페이지·coverage는 유지한다.

**권한:** 기존 `Handler.authorize` → `kube.Authorizer`의 Bearer TokenReview/SAR를 통과한다. SAR path는 기존처럼 `/chill`을 실제 요청 경로 앞에 붙인다. 따라서 하네스 전용 ClusterRole의 `nonResourceURLs`에는 **`/chill/api/v1/observations/raw`와 `/chill/api/v1/observations/raw/*` 둘 다**, verb `get`을 넣는다. 아래표의 `/raw`, `/raw/*`는 이 전체 경로의 축약이다. 루트 목록은 wildcard 하나로 보호/허용되지 않는다. Dashboard/BFF·일반 serving-manager·공개 앞단의 allowlist에는 raw를 추가하지 않으며 토큰을 공유하지 않는다. 앞단 비노출과 별개로 서버 직접 호출도 SAR로 거절한다.

### 17.3 자원 기록 (`SCH-68`)

**수집 위치:** 기존 기본 discovery node-agent에 채집을 붙인다. actuation DaemonSet은 쓰지 않는다. 모든 대상 Node(genesis1 포함)에 기본 agent가 있어야 하고 현재 exclude/selector가 서버 Node를 빼지 않는지 배포 때 확인한다. 서버 컨테이너도 genesis1 agent가 밖에서 잰다. 서버가 자기 cgroup을 읽으면 GC/잠금 정지 때 자기 측정도 멈추므로 그 경로는 없다.

**Pod 권한 없는 대상 발견:** 서버는 기존 Kubernetes 읽기 권한으로 CHILL 서빙 인스턴스와 제어 Pod의 현재 Node·Pod UID·containerID를 고른다. 범위는 해당 ChillSystem 소유 서빙 Pod와 릴리스의 CHILL 제어 Pod이며 load-generator/client·다른 tenant 컨테이너는 제외한다. node-agent에는 Pod get/list/watch를 추가하지 않는다. 내부 POST 응답의 `targets`가 해당 Node의 정확한 `{podUID,containerID}` 집합을 다음 묶음에 공급한다. 첫 POST는 `samples:[]`로 대상만 얻으며 이 handshake 자체는 resources 행을 만들지 않는다. 새 Pod를 알기 전 구간은 소급 수집하지 않는다.

agent는 읽기 전용 호스트 `/sys/fs/cgroup` 트리를 kubelet의 systemd/cgroupfs 명명 규칙으로 직접 탐색해 받은 Pod UID와 containerID가 모두 일치하는 실물 디렉터리만 읽는다. private cgroup namespace에 상대적인 `/proc/<pid>/cgroup`·mountinfo에는 의존하지 않으며, hybrid는 v1 controller(cpu/cpuacct·memory)를 우선하고 v2는 unified-only 환경에서만 사용한다. 발견 실패·controller 결손은 사유로 남기며 같은 Pod 이름/slot이나 루트 counter로 대체하지 않는다. Pod 종료·재시작·image 판정은 하지 않는다(cluster 소유).

**전송 경계:** `POST /api/v1/internal/observations/resources` 한 개, 요청 `{node:{name,uid},samples:[{sampledAt,containers:[...],thermal:[...]}]}`, 응답 `{targets:[{podUID,containerID}]}`다. 서버는 요청 크기를 기존 MaxRequestBytes로 제한하고 Node identity·배열 크기·시각·counter/온도 형식과 target membership을 경계에서 검증한다. 컨테이너 교체 중 이미 채집한 표본은 서버가 그 agent에 직전 응답으로 공급한 identity에 속하면 허용한다. 새 UID를 옛 UID로 바꿔 연결하지 않는다. 응답 후 다음 묶음은 새 targets를 사용한다. 서버 재시작으로 이전 targets를 확인할 수 없으면 묶음을 거절하고 agent가 다음 빈 handshake로 재동기화한다. 과거 표본 재전송은 하지 않는다.

- TLS 서버 신뢰 CA·고정 내부 URL·projected SA token 파일을 기본 agent 설정으로 전달한다. 토큰 rotation을 읽고 검증을 끄지 않는다. 기존 TokenReview/SAR를 재사용하고 agent SA에 정확한 `/chill/api/v1/internal/observations/resources`의 `post`만 추가한다. raw GET·settings PUT·Pod 권한은 주지 않는다. HTTP 헤더나 본문의 username을 신뢰하지 않는다. 공유 DaemonSet SA는 Node별 암호학적 신원을 증명하지 않으므로 본문 Node를 TokenReview가 보증한 것으로 표시하지 않는다.
- 표본 주기 기본 `1s`, 묶음 주기 기본 `10s`를 수집 설정으로 제공하고 실행 조건에 고정한다. Node당 한 POST/한 resources 행에 묶음 표본을 넣는다. 주기마다 새 묶음을 보내며 요청은 유한 timeout을 갖는다. **실패 묶음은 버리고 재전송·spool·backoff queue를 만들지 않는다.** 실패 후 다음 주기는 새 표본/handshake만 보낸다.
- 비어 있지 않은 묶음은 서버 append 성공일 때만 200과 다음 targets를 보낸다. 빈 handshake는 append 없이 targets를 반환한다. append 실패는 기존 log health와 오류 응답으로 드러낸다. 응답만 유실되어도 같은 묶음을 다시 보내지 않는다. 수신 순서로 기록하며 Node 표본 시각으로 저장 분을 역선택하지 않는다.

**저장 행:** 여섯 stream은 동일 ObservationLog writer를 쓰며 다음 새 resources 행은 `<write-hour>/resources.<write-minute>.jsonl`에 들어간다. Node 묶음은 `kind:"node"` 한 행이고 서버 누적값은 별도 `kind:"server"`다.

| 행/필드 | 의미·단위 |
|---|---|
| node: `node`, `receivedAt`, `samples[]` | Node 이름/UID, 서버 수신 UTC 시각, 원래 묶음. 각 `sampledAt`은 Node 시계 RFC3339Nano. 두 시각을 남기되 시계 보정 없음 |
| samples의 `containers[]`: `podUID`, `containerID`, `cgroupVersion` | 샘플 대상 실물 identity. containerID는 runtime 접두어를 포함한 원래 식별자, cgroupVersion은 1 또는 2 |
| `cpuUsageNanos`, `throttledNanos`, `periods`, `throttledPeriods` | cgroup 누적 counter. v1/v2 원 단위를 각각 ns/횟수로 명시 변환하며 CPU core 사용률로 미리 집계하지 않음 |
| `memoryCurrentBytes`, `memoryPeakBytes` | cgroup 현재/최고 사용 bytes. 지원하지 않거나 읽지 못한 값은 필드 부재와 해당 `errors` 사유; peak를 current로 대체하지 않음 |
| `thermal[]:{sensor,milliCelsius}`, `errors[]` | host sensor의 실제 식별(thermal zone 이름/경로와 type), 원래 온도 단위를 milli°C로 맞춤. 센서 없음/읽기 실패를 0°C로 만들지 않음. 부분 채집 실패는 대상·필드·이유로 남김 |
| server: `at`, `podUID`, `processStartedAt`, `gcCPUSeconds`, `gcCycles` | 서버 시각·Pod UID·프로세스 시작 시각, `/cpu/classes/gc/total:cpu-seconds`와 `/gc/cycles/total:gc-cycles` 누적값. 재시작을 가로질러 빼지 않음 |
| server: `observationDirectoryBytes`, `directoryObservedAt` | 분 1회 관측 디렉터리 전체 regular file 논리 크기 합(bytes, 루트 definitions 포함). PVC capacity/allocated blocks/압축 archive 크기와 다르며 `/tmp` scratch는 제외. 측정한 tick에만 싣고 매초 복제하지 않음. stat 실패는 값 대신 이유 |

서버 누적값은 기존 live/runtime **1초 ticker**에 붙이고 다른 1초 loop를 만들지 않는다. 분 단위 디렉터리 순회는 writer mutex 밖에서 수행하며 동시 append/삭제가 있는 시점 관측이라는 의미를 유지한다. `processStartedAt`과 Pod UID로 창 안 누적 차를 구하고 gcCycles 감소를 임의 0으로 보정하지 않는다. 원 gctrace 로그와 지표 이름/의미가 같다고 주장하지 않는다.

하네스 수집 가능 시점은 drain 후 **측정 창 끝 + 묶음 주기 + 다음 분 경계** 이후다. resources 행은 서버 수신 시각의 분에 쓰이므로 이 여유와 늦게 받은 묶음의 실제 수신 시각을 함께 고려해 원시를 받는다. 관측 시계 차이는 두 시각으로 사후 확인하며 새 동기화 프로토콜은 없다.

### 17.4 클러스터 기록과 라이브 callback (`SCH-69`)

기존 server-side Pod LIST/WATCH와 observer issue 변경 지점을 사용한다. 서빙 Pod뿐 아니라 CHILL 제어 Pod도 포함하고 terminal Pod를 제외하는 기존 서빙 조회만으로 전체 기록을 대신하지 않는다. 초기 LIST는 `observed` baseline이며 생성 사건을 보았다고 꾸미지 않는다. 이후 add/change/delete와 상태 변화를 기록한다. 읽기 오류·watch 단절을 Pod 삭제로 합성하지 않는다. server 시작에는 Downward API로 받은 자기 Pod UID를 `server-start` 한 행으로 먼저 남긴다.

| cluster 행 | 필수 내용 |
|---|---|
| `{kind:"pod",at,event,pod:{...}}` | `event`는 `observed`, `created`, `changed`, `deleted`, `server-start` 중 하나. Pod namespace/name/UID, Node 참조, creationTimestamp·deletionTimestamp, phase·Ready 상태, init/일반 container별 name·containerID·imageID·restartCount·현재/직전 terminated reason/exitCode/시각. 아직 없는 필드는 미확정으로 남김. image tag를 imageID/digest로 대신하지 않음 |
| `{kind:"issue",at,event,source,reason,...}` | `event`는 `raised`, `changed`, `resolved` 중 하나. 기존 ObserverIssue의 정확한 source identity(Pod UID/containerID를 포함한 source면 유지), 이유, 감지 서버 시각. changed는 이전 이유도, resolved는 해소한 이전 이유도 남김 |

issue의 동일 source/이유 반복은 발생 시각을 매 refresh 갱신하며 새 사건으로 쌓지 않는다. 없던 issue→raised, 이유 변경→changed, 실제 성공 확인→resolved다. source 읽기에 실패한 상태를 임의 해소하지 않는다. 기록 hook 자체의 오류를 다시 issue hook으로 넣는 재귀 경로도 만들지 않는다.

**공유 인터페이스:** core에 `ClusterObservation` DTO와 `(*ObservationLog).ClusterRecord(ClusterObservation) error`를 둔다. `kube.ObserverConfig.OnCluster func(serving.ClusterObservation) error`로 Pod·issue 사실을 composition root에 넘긴다. 그 root의 `recordCluster`는 **ClusterRecord 성공 → livePublisher.cluster(같은 행)** 순서이며 기존 `schedulerService.recordDecision/recordAction`과 같다. core는 live DTO나 kube 타입을 import하지 않는다. callback에서 observer/controller를 다시 조회하지 않는다.

append 실패는 기존 ObservationLog health·호출 오류/로그로 노출하고 라이브를 발행하지 않는다. 관측의 실제 상태 메모와 마지막 기록된 projection을 구분하여 실패한 append를 기록 완료로 표시하지 않는다. 다음 정상 관측에서 확인된 상태를 기록할 수 있으나 별도 영속 outbox·재전송 큐·누락 사건 복구를 약속하지 않는다. 초기 baseline이 과거 이벤트 이력의 완전성을 보장하지도 않는다. hub 발행 실패 때문에 이미 저장된 행을 재append하지 않는다.

라이브 wire event 형태·화면은 [대시보드 라이브 계약](../dashboard/README.md#3-라이브-api-계약-v1)이 권위이며 [완료 계획 요약](../../plans/2026-10-26-dashboard-live-stage.md)은 이행 근거다. 이 슬라이스는 전달 지점과 DTO 투영만 연결한다. Node scheduler 기록·seq/resync/heartbeat·인증을 재사용하고 resources 원시 전체나 raw 권한을 브라우저에 주지 않는다.

### 17.5 읽기 전용 settings `meters` (`SCH-70`)

`GET /api/v1/scheduler/settings`에 `meters:[]`를 추가한다. 값은 scheduler service가 metric-sources와 실제 Node 이름/UID로 **수락한 configuration snapshot**의 `[]MeterDeclaration`이다. 항목은 기존 `{id,node:{name,uid},scope,source}`를 그대로 투영하고 선언이 없으면 빈 배열이다. source는 수락한 전력 출처 선언(kind/nodeName/endpoint/metric/metricLabels)이지 토큰·비밀 설정이나 실시간 표본이 아니다. API가 ConfigMap의 원문을 별도로 읽어 두 번째 선언 원본을 만들지 않는다.

- `HandlerConfig.SchedulerMeters func() []servingkube.MeterDeclaration`를 root에서 accepted configuration 조회에 연결한다. settings GET에서만 읽기 projection에 붙이고 `SchedulerSettingsInput`, scheduler 저장 ConfigMap, settings mutation 본문에는 넣지 않는다. 기존 `powerObservation` 경계는 바꾸지 않는다.
- `meters`가 든 PUT은 기존 strict unknown-field 거절로 실패시킨다. 서버가 조용히 버리거나 쓰기 별칭으로 수락하지 않는다.
- 같은 구현 1에서 `hack/e5/e5.py:CPO.put`, `chill-dashboard/features/scheduler-settings.tsx:putSchedulerSettings`가 GET 결과에서 `meters`를 제거한 뒤 PUT하도록 고친다. 기존 staticRouting/staticRoutingValidatedAt 제거와 CPO의 powerObservation 처리도 보존한다. 대시보드 read type에는 읽기 전용 선언을 추가한다.
- 기존 `hack/e5/d2/conduct_before.py`는 과거 before 실행 기록 도구이며 신규 API 수집·설정 쓰기 경로로 재활성화하지 않는다. 클라이언트 두 곳을 고치지 않은 채 GET 확장만 배포하지 않는다.

### 17.6 병렬 구현 3개: 파일 영역과 인터페이스

기존 core 안의 생산자별 DTO/append와 composition root wiring을 사용한다. 새 production package·중복 schema·generic event bus·단일 구현 interface·중간 stub은 없다. `observation_log_*.go`는 `internal/serving/`, `httpapi/`와 `kube/`도 그 하위 경로다.
| 영역 | 현행 인터페이스·소유 경계 |
|---|---|
| raw + meters | ObservationLog의 `RawHour(ctx,hour) (RawHourIndex,error)`, `OpenRaw(ctx,hour,kind) (RawObservation,error)`, `OpenRawDefinitions(ctx) (RawObservation,error)`. RawObservation=`{Reader io.ReadCloser,ContentType,Bytes}`, caller가 Close. raw/httpapi와17.5의 accepted meters·PUT clients, 공통 stream 등록/route/deadline/root wiring은 통합 영역 |
| resources | `ResourcesRecord(ResourceObservation) error`, `HandlerConfig.ReceiveResources func(context.Context,serving.ResourceBatch) ([]serving.ResourceTarget,error)`. root callback이 현재/직전 발급 target 메모·검증→receivedAt→비어 있지 않은 묶음 append→다음targets를 소유한다. collector는 typed 행만 append하고 raw API를 호출하지 않음. agent 수집/전송과 기존 ticker의 GC/디렉터리 관측은17.3 계약 |
| cluster + live | `ClusterRecord(ClusterObservation) error`, `ObserverConfig.OnCluster func(ClusterObservation) error`, root recordCluster→append성공→publisher.cluster. resources target 선정은 독립 소유이며 cluster 저장 성공을 수집 전제로 삼지 않음 |
생산자는 기존 잠금·rollback·health를 사용하고 raw reader는 producer를 import하거나 기다리지 않는다. 공통 wiring·권한·배포는 통합 소유자가 맡는다.

권한/배포 공통 변경(`charts/chill/templates/serving_entrypoint_rbac.yaml`, serving Pod UID env, 라이브·S1 권한, 서버·대시보드·node-agent 이미지)은 통합 소유자 한 명이 맡는다. raw 권한은 하네스 전용, 수신 POST 권한은 기본 agent 전용이다. 일반 serving-manager의 `/observations/*`, `/api/v1/*` 같은 광범위 권한이 raw나 내부 수신 POST까지 열지 않도록 관련 grant를 가공/관리 경로별로 좁힌다. 실제 승인 배포 전에 RBAC를 확인하며 단순히 새 narrow role을 추가했다고 기존 wildcard 허용이 사라지는 것은 아니다.

### 17.7 이행·수용·미확인 경계

문서 계약은 `e699801c`(SCH-67~70/SCH-AC-52~55/ADR0030), 구현 전제는 `e947cd5e`의 단일 ChillServer이며 gateway 재개는 없다. 승인 배포는 raw/agent POST와 [대시보드 라이브·S1 권한 이행](../../plans/2026-10-26-dashboard-live-stage.md), 이미지·agent설정/mount/보안·genesis1·edge TLS를 함께 다룬다. cutover는17.1대로 입력을 한 번에 옮기되 client/STOP/장애주입은 유지한다.
수용은 단1회 old/new 동시 수집 리허설과 #213 보관본1시간 API 재판정의 D1·지연·J 일치, resources identity/단위/결손, GC 의미 변경 표시, 서버 CPU의 live hub/agent수신/raw/대시보드 몫 기록이다. 옛 경로를 제거하고 이 비교를 상시 관문으로 남기지 않는다. 실제 결과/미완료 경계는17.8절이다.

| 구현 시 남길 최소 검사 | 연결 수용 기준 |
|---|---|
| 닫힌 분만 export·정의 torn tail 제외·fd 확보 뒤 archive 삭제·기존 #213 prefix 조회·과거 두 stream 부재·404/409/store 오류/취소/긴 전송·root/wildcard RBAC 분리 | SCH-AC-52 |
| cgroup v1/v2 실제 mount/identity 탐색·counter 단위·미지원 값·Node 시계 차이·전송 실패 무재전송·Pod 권한 없음·container 교체·GC process reset·분별 directory bytes | SCH-AC-53 |
| SCH-68 resources 수집·수신·누적 기록은 해당 행을 추가한 구현 커밋, 공통 등록/ticker/배포 설정은 통합 커밋; 생산자 인접 resources_test.go | 코드 이행이며 환경 실측 수용 아님 |
| 초기 baseline/자기 Pod UID·Pod/컨테이너 상태 변화·issue 발생/변경/해소·동일 반복 억제·append 실패 시 live 미발행·저장 성공 후 동일 callback | SCH-AC-54 |
| accepted meters projection·GET 후 CPO/대시보드 PUT에서 제외·직접 meters PUT 거절·기존 읽기 전용 필드 경계 유지 | SCH-AC-55 |
| `feat(serving): expose raw evidence and accepted meters (SCH-67 SCH-70)`은 raw/httpapi·원래 prefix 두 stream·마감·meters/두PUT client와 resources/ticker/cluster root wiring을 연결. resourceObservation bounds/주기/release·system·control scope는 values/flags, TLS URL/CA/token은 기본 discovery config. focused raw/settings/service 검사·e5.py self-check는 추가 당시 worker 미실행 | SCH-AC-52~55 코드 이행, 당시 클러스터 접근/배포0 |
| 통합 gate는 v1alpha5 시간경로·실제 네 stream archive 재시도·미기록 resources/cluster 부재를 검사하도록 보정; 생산자/저장 의미 변경 없음 | SCH-AC-52·53 검사 전제 |

통합 gate 보정 후 `make lint LOCALBIN=/home/genesis1/ETRI/chill/bin`과 `go test -p 1 ./cmd/chill-server ./internal/serving ./internal/serving/httpapi ./internal/serving/kube ./cmd/node-agent`가 PASS했다(`flock /tmp/chill-gotest.lock`, `nice -n 19`, `GOMAXPROCS=2`). lint에서 드러난 자원 수신·옵션 검증 복잡도는 기존 검증을 보존한 함수 분리로 낮췄다. 이는 코드 gate 결과이며 배포·실측 수용을 대신하지 않는다.

구현 2의 **배포 시 1회 확인**할 환경 사실은 (a) 비root 기본 agent가 필요한 cgroup 파일을 읽을 수 있는지, (b) edge Node에서 서버 내부 TLS endpoint까지 닿는지, (c) 작은 Node의 1초 표본 오버헤드다. 이번 코드 이행에서는 클러스터 접근·배포·실측을 수행하지 않는다. 확인 전 비root 가능·네트워크 도달·허용 오버헤드를 기정사실로 쓰지 않는다. 실패 시 수집을 가짜 0으로 통과시키거나 actuation 특권을 통째로 넘기지 않고 필요한 읽기 권한/배포 조건과 실측 비용을 명시한다. 최종 완료는 CHILL 사실의 API 단일 출처와 네 수용 시나리오·리허설 증거이며, 문서·코드 이행·배포·실험 수용은 서로 구분한다.

### 17.8 운영 배포 결정 (#226/#227, 2026-10-30)

rev106은 새 서버·대시보드·node-agent 이미지와 config를 게시했으나, 기존 operator `pools-control-188-e3431ff391a7`가 `resourceCAFile`을 strict unmarshal에서 거부하여 소유 DaemonSet을 갱신하지 못했다. ConfigMap watch는 이미 `internal/operator/nodeagent/setup.go`에 있고 실제 reconcile 오류도 반복 관측했으므로 checksum annotation·재시작·ChillSystem touch를 추가하지 않는다. Main 승인 #227에서 새 schema·mount를 아는 operator를 정식 Helm upgrade로 배포하고, rev106의 나머지 이미지 digest는 유지한다. 이후 모든 Node의 새 agent digest/Ready와 API200을 확인한 뒤에만 1회 리허설을 시작한다. 실패 원형은 `O/deployB/control-226-20261030/`, 별도 조회 API200·operator strict 오류와 리허설 증거는 `O/campaign/v2/d-20261027/control-226-rehearsal/`에 보존한다.

rev107 뒤 새 agent는 전 Node Ready였지만 edge의 Service DNS가 NXDOMAIN을 반환했고 기본 URL의 `/chill` prefix도 실제 HTTP handler와 달랐다. Main 승인 #228은 기본 handler 경로를 `/api/v1/internal/observations/resources`로 바로잡고, 기존 `nodeAgent.resourceObservation.url` values에 CA SAN으로 검증되는 `https://155.230.35.213:31104/api/v1/internal/observations/resources`를 명시한다. chart-only Helm upgrade라 rev106/107 이미지 digest를 재빌드하지 않는다. 전송 실패 구간은 resources 결손으로 남기며 소급 표본·TLS 우회·자동 측정 재시도는 하지 않는다.

#229는 agent의 고정 URL 검증에서도 SAR 전용 `/chill` prefix를 제거한다. HTTP handler는 `/api/v1/internal/observations/resources`, Kubernetes SAR 권한은 `/chill/api/v1/internal/observations/resources`로 의도적으로 다르다. `TestResourceEndpointUsesHTTPHandlerPath`는 정확한 HTTP 경로가 CA 로드까지 도달하고 SAR 경로는 거부되는지 검사한다. node-agent만 새 multiarch 이미지로 배포하며 같은 경로/URL 불일치가 다시 발생하면 리허설을 시작하지 않고 중단한다.

#229 자원 결손은 비root 읽기 권한이 아니라 private cgroup namespace의 `/host/proc/<pid>/cgroup=0::/`·상대 mount root와 Jetson hybrid의 v2 우선 선택에서 발생했다. node-agent는 호스트 cgroupfs 실물 identity 탐색·v1 우선으로 수정하며 hostPID·권한 확대 없이 새 multiarch 이미지 재빌드가 필요하다(코드 회귀와 클러스터 배포·실측 수용은 별개).

Main 사전 승인 #230의 `d01ff310` multiarch agent를 rev110으로 배포한 뒤 9개 Node에서 resources 묶음을 받았다. 비root UID65532의 CPU·memory 실물 읽기는 genesis1/2/3·jetsono·jetson-nano(v1), rasp5·orangepi·lattepanda(v2)에서 확인했다. jetsonx는 묶음/thermal이 오지만 컨테이너 cgroup identity 탐색 결손이 남는다. 일부 kernel의 `memory.peak` 부재와 thermal 센서 읽기 실패도 원래 오류로 남긴다. orin-nano는 새 agent Ready·권한/CA/TLS·동일 net namespace의 Kubernetes Node GET200/빈 resources handshake200을 확인했으나 실제 수집 loop에서 출력/표본이 없었다. 초기 Go Node GET 대기는 [INFERENCE]이며 확정 원인으로 적지 않는다. **Main은 E5 excludedNodes/POOL 외인 orin-nano의 이 결손을 명시적으로 유지하고 이번 1회 리허설의 전제에서 제외하도록 승인했다.** 전체 Node 관측 수용을 PASS로 바꾸지 않는다.

#226 단일 리허설은 E0 Ready3/3, MIX 60초(MNV/PLC/Smol offered 1177/2385/12·HTTP 성공100%)와 두 창의 전력 완결을 확인했다. 같은 원시를 host-s3/API로 각각 줄인 D1·지연·전력 J는 완전 동일했고, #213 E5-2도 API 보관본 재판정이 원판정 FAIL·동일 KPI였다(최초 오프라인 provenance 누락은 보존 후 Main 승인으로 교정). 다만 finish는 잔류 owner `plan-d46bdffb3060` 예약1로 600초 timeout되어 **운영 preflight FAIL을 그대로 보존**했다. 신규 Ready Pod라는 known-effect와 prepare partial/context canceled·현재 보상 action 부재를 기록한 뒤 Main 승인 takeover reset 1회로 instances0/reservations0·설정복귀·Job 정리를 완료했으며 측정 재시도는 0회다.

MIX의 resources 컨테이너 표본 1600건은 cluster의 Node/Pod UID/containerID와 전부 연결되고 counter 타입/단위 오류0이었다. 1초 표본을 켠 discovery agent 전체 cgroup CPU는 rasp5(v2) 0.002370 core(1core의 0.2370%), jetson-nano(v1) 0.045047 core(4.5047%)였으며 이는 추가분의 인과 추정이 아닌 전체 agent 비용이다. 측정 창 밖 60초 CPU profile(총10.98 CPU초)에서 라이브 경로 0.27초/2.459%, node-agent 수신0.80초/7.286%, raw 전송0.12초/1.093%, 대시보드 조회1.85초/16.849%의 서버 CPU 몫을 기록했다. 공통 GC/TLS/auth 등은 나누지 않았고 100Hz 표본의 미검출을 비용0으로 해석하지 않는다. 근거·실패 원형·정확 비교·원시/profile은 `O/campaign/v2/d-20261027/control-226-rehearsal/`에 보존한다. 하네스 cutover와 브라우저 수용은 이번 작업에서 수행하지 않았다.

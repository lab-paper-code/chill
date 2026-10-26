# U2 전력모드 커버리지와 최소 측정 순서 (2026-10-27)

## 1. 계약·입력·읽기 경계

`PC-01..03`([catalog SRS](../spec/catalog/SRS.md#6-u2-전력모드-평가-커버리지)), [구성 선택](../spec/04_configuration_selection.md), [연속 진행 §5](2026-10-27-pre-e5-workflow.md#5-사용자-수정2026-10-26-203xz-대기-없이-연속-진행)을 구체화한다. 최소안은 별도 승인 대기 없이 CampaignOps가 기존 측정 종료·반환 뒤 직렬 실행한다. 이 문서 작성은 **클러스터 변경·측정·빌드 0회**이며, 측정 완료나 E5 시작 승인이 아니다.

`O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`. 입력:

- 인증된 `GET https://155.230.35.213:31104/api/v1/scheduler`의 `state.snapshot.at=2026-10-26T20:33:57.347740004Z`: options **69**, costs **153**, baselines **75**. 선언 모드는 읽기 전용 `kubectl get deviceclasses.edge.dacs.io -o json`으로 확인했다. 토큰은 문서에 남기지 않는다.
- 원시 캡처 `O/intent/u2-power-coverage-live-20261027.json`; 69개 option별 Node UID, profile ID/reference, CPU affinity/allocation, runtime, 배치, d source, 준비·제거/유휴/전환 references는 `O/intent/u2-power-coverage-rows-20261027.json`. 이 JSON은 카탈로그 보유 사실이지 재인증 결과가 아니다.
- 이전 비교점 `O/campaign/v2/exp-20261027-window-6/scheduler.json`의 `2026-10-26T14:19:22.647859253Z`, `O/intent/e5-numbers-20261027.md` §§2–3: 새 규칙 d **10/34**, 재인증 대기24, 정확 비용69/69. 최근 GET에는 PLC 일부 d가 추가되어 있어 이전 10/34를 현재 누계라고 쓰지 않는다. 새 규칙 수용 여부는 CampaignOps의 해당 CR terminal·raw·consumer 3종으로 확인한다.

현재 물리 모드(jetsono=MODE_15W, jetsonx=MODE_20W_2CORE, nano=MAXN)와 **실행 프로파일이 있는 모드**는 다르다. 물리 모드를 프로파일 모드라고 오인하지 않는다. genesis1/orin-nano는 E5 대상8대에서 제외하며 orin-nano의 선언15W/25W/MAXN_SUPER/7W는 범위 밖이다. U3의 genesis 신규 클럭은 아직 live 선언 fixed 밖이므로 §5의 조건부 확장에 둔다.

## 2. 보유/누락 표

### 2.1 실행 프로파일이 있는 모든 조합

M=`mobilenet-v2-050`, P=`plc-lstm-ae`, S=`smolvlm2-500m-video-instruct`. `qN:배치`는 해당 CPU quota 변형의 측정 배치 목록이며 세미콜론으로 **서로 다른 profile**을 구분한다. 동일 q/배치여도 FP32/INT8/runtime 차이가 있어 합치지 않는다. 원시 JSON이 정확한 식별자·할당을 보존한다. `P`는 실행 profile 수, `d`는 **정확 Node UID+모드+모델+runtime 일치 source의 보유 수/P**로 구규칙을 포함한다. 새 규칙 Accepted 수가 아니다.

다섯 근거 중 준비·제거 `C`는 **아래 모든 행 P/P 보유**(69/69, profile ID가 cost from/to에 정확히 등장하는 양쪽 종류). `I`는 profile ID가 loaded baseline state에 직접 등장하는 수/P로, 빠진 칸을 다른 profile 유휴로 자동 대체하지 않는다. 모든 노드·현재 프로파일 모드의 unloaded baseline은 보유. `T`는 §2.2 모드 쌍 표를 적용한다. fixed는 다른 선언 모드가 없어 T=해당 없음이다. 배치마다 d를 별도 복제해 센 것이 아니며 d의 공유 범위는 현행 계약을 따른다.

| Node | 모드 | 모델 | 실행 backend | CPU quota:측정 배치 | P | d 보유/P | I 보유/P |
|---|---|---|---|---|---:|---:|---:|
| genesis2 | fixed | M | ort-cpu | q1:1,2; q4:1,2 | 2 | 2/2 | 1/2 |
| genesis2 | fixed | M | ort-cuda | q1:1,2 | 1 | 1/1 | 0/1 |
| genesis2 | fixed | P | ort-cpu | q1:1,2,4,8,16,32; q2:1,2,4,8,16,32; q4:1,2,4,8,16,32 | 3 | 3/3 | 3/3 |
| genesis2 | fixed | P | ort-cuda | q1:1,2,4,8,16,32 | 1 | 0/1 | 1/1 |
| genesis2 | fixed | S | ort-cpu | q4:1,2,4; q4:1,2; q1:1,2; q2:1,2,4 | 4 | 4/4 | 4/4 |
| genesis3 | fixed | M | ort-cpu | q1:1,2; q2:1,2; q4:1,2,4 | 3 | 3/3 | 1/3 |
| genesis3 | fixed | M | ort-cuda | q1:1,2 | 1 | 1/1 | 1/1 |
| genesis3 | fixed | P | ort-cpu | q1:1,2,4,8,16,32; q2:1,2,4,8,16,32; q4:1,2,4,8,16,32 | 3 | 0/3 | 3/3 |
| genesis3 | fixed | P | ort-cuda | q1:1,2,4,8,16,32 | 1 | 1/1 | 1/1 |
| genesis3 | fixed | S | ort-cpu | q4:1,2,4; q4:1,2; q1:1,2; q2:1,2; q8:1,2,4 | 5 | 5/5 | 5/5 |
| jetson-nano | MAXN | M | ort-cpu | q1:1,2 | 1 | 1/1 | 0/1 |
| jetson-nano | MAXN | M | ort-cuda | q1:1,2,4,8,16 | 1 | 0/1 | 1/1 |
| jetson-nano | MAXN | P | ort-cpu | q1:1,2,4,8,16,32; q2:1,2,4,8,16,32; q3:1,2,4,8,16 | 3 | 0/3 | 3/3 |
| jetson-nano | MAXN | P | ort-cuda | q1:1,2,4,8,16,32 | 1 | 0/1 | 1/1 |
| jetson-nano | MAXN | S | ort-cpu | q2:1,2; q3:1,2; q1:1,2 | 3 | 3/3 | 3/3 |
| jetsono | MAXN | M | ort-cpu | q1:1,2 | 1 | 1/1 | 0/1 |
| jetsono | MAXN | M | ort-cuda | q1:1,2,4,8,16 | 1 | 0/1 | 1/1 |
| jetsono | MAXN | P | ort-cpu | q1:1,2,4,8,16,32 | 1 | 0/1 | 1/1 |
| jetsono | MAXN | P | ort-cuda | q1:1,2,4,8,16,32 | 1 | 0/1 | 1/1 |
| jetsono | MAXN | S | ort-cpu | q4:1,2,4; q4:1,2 | 2 | 2/2 | 2/2 |
| jetsono | MAXN | S | ort-cuda | q1:1,2 | 1 | 1/1 | 1/1 |
| jetsonx | MODE_15W_6CORE | M | ort-cpu | q1:1,2 | 1 | 1/1 | 0/1 |
| jetsonx | MODE_15W_6CORE | M | ort-cuda | q1:1,2,4,8 | 1 | 0/1 | 1/1 |
| jetsonx | MODE_15W_6CORE | P | ort-cpu | q1:1,2,4,8,16,32 | 1 | 0/1 | 1/1 |
| jetsonx | MODE_15W_6CORE | S | ort-cpu | q2:1,2 | 1 | 1/1 | 1/1 |
| lattepanda | fixed | M | ort-cpu | q1:1,2; q2:1,2 | 2 | 2/2 | 0/2 |
| lattepanda | fixed | P | ort-cpu | q1:1,2,4,8,16,32; q2:1,2,4,8,16,32 | 2 | 0/2 | 2/2 |
| lattepanda | fixed | S | ort-cpu | q2:1,2,4; q1:1,2 | 2 | 2/2 | 2/2 |
| orangepi | fixed | M | ort-cpu | q1:1,2; q2:1,2,4,8,16; q4:1,2,4,8,16 | 3 | 3/3 | 2/3 |
| orangepi | fixed | P | ort-cpu | q1:1,2,4,8; q2:1,2,4,8,16,32; q4:1,2,4,8,16,32 | 3 | 3/3 | 3/3 |
| orangepi | fixed | S | ort-cpu | q4:1,2; q4:1,2; q2:1,2; q6:1,2,4 | 4 | 4/4 | 4/4 |
| rasp5 | fixed | M | ort-cpu | q1:1,2; q1:1,2; q2:1,2 | 3 | 3/3 | 0/3 |
| rasp5 | fixed | P | ort-cpu | q1:1,2,4,8,16,32; q2:1,2,4,8; q3:1,2,4,8,16,32 | 3 | 0/3 | 3/3 |
| rasp5 | fixed | S | ort-cpu | q1:1,2; q2:1,2; q3:1,2 | 3 | 3/3 | 3/3 |

S CUDA는 genesis2/3에서 알려진 OOM 때문에 제외하며 jetsonx/nano에는 이 카탈로그의 실행 profile이 없다. orangepi NPU도 이 69개에는 없다. 누락을 성능 0으로 해석하지 않는다.

### 2.2 전체 선언 모드와 추가 모드의 다섯 근거

각 추가 모드 행의 M/P/S는 **각각 별개 모델 행**을 줄여 쓴 것이다. 최소 실행 방식은 CPU ORT(S는 INT8), q1/B1. 중간·전체안의 추가 CUDA/배치는 §5에 정의한다. `—`=누락, `보유`=catalog 참조 있음. 수용 상태와 보유는 구분한다.

| Node | 선언 모드 | M/P/S 실행 profile | d | 준비·제거 | unloaded / loaded idle | T(직접 방향) |
|---|---|---|---|---|---|---|
| jetsono | MAXN | §2.1, S CPU q1은 누락 | §2.1, M CPU는 구규칙 | 기존 profile분 보유, S q1 누락 | 보유 / §2.1 | MAXN→15W, 15W→MAXN 보유 |
| jetsono | MODE_15W(최소) | 세 모델 모두 누락 | — | — | 보유 / — | MAXN과 양방향 보유 |
| jetsono | MODE_30W | 세 모델 모두 누락 | — | — | 보유 / — | 15W→30W만 보유, 역방향 누락 |
| jetsono | MODE_50W | 세 모델 모두 누락 | — | — | 보유 / — | 30W→50W만 보유, 역방향 누락 |
| jetsonx | MODE_15W_6CORE | §2.1, S CPU q1은 누락 | §2.1 | 기존 profile분 보유, S q1 누락 | 보유 / §2.1 | 15W4C와 직접 양방향 누락 |
| jetsonx | MODE_15W_4CORE(최소) | 세 모델 모두 누락 | — | — | 보유 / — | 15W4C→20W4C만 보유 |
| jetsonx | MODE_15W_2CORE | 세 모델 모두 누락 | — | — | 보유 / — | 10W2C→15W2C, 15W2C→20W2C 보유 |
| jetsonx | MODE_10W_2CORE | 세 모델 모두 누락 | — | — | 보유 / — | 10W2C↔15W6C 보유 |
| jetsonx | MODE_10W_4CORE | 세 모델 모두 누락 | — | — | 보유 / — | 20W2C→10W4C, 10W4C→10WDESKTOP 보유 |
| jetsonx | MODE_10W_DESKTOP | 세 모델 모두 누락 | — | — | 보유 / — | 10WDESKTOP→15W4C 보유 |
| jetsonx | MODE_20W_2CORE | 세 모델 모두 누락 | — | — | 보유 / — | 20W2C↔15W6C 보유 |
| jetsonx | MODE_20W_4CORE | 세 모델 모두 누락 | — | — | 보유 / — | 20W4C→20W6C 보유 |
| jetsonx | MODE_20W_6CORE | 세 모델 모두 누락 | — | — | 보유 / — | 들어오는 방향만 보유, 역방향 누락 |
| jetson-nano | MAXN | §2.1 | §2.1 | 보유 | 보유 / §2.1 | MAXN↔5W 양방향 누락 |
| jetson-nano | 5W(최소) | 세 모델 모두 누락 | — | — | — / — | 양방향 누락 |
| genesis2, genesis3 | fixed | §2.1 | §2.1 | 보유 | 보유 / §2.1 | 해당 없음, U3 추가 모드는 아직 미선언 |
| lattepanda, rasp5, orangepi | fixed | §2.1 | §2.1 | 보유 | 보유 / §2.1 | 해당 없음 |

기존 power cost는 빈 Node의 방향별 비용이다. 모델을 적재한 상태의 전환 비용·Ready 시간이 측정됐다고 주장하지 않는다. §4는 모든 모델을 제거한 뒤 모드를 변경하는 선택의 근거를 갖춘다. 서로 다른 loaded 상태의 직접 전환 후보는 별도 측정 전까지 평가 대상에 넣지 않는다. 선언돼 있어도 2CORE는 예약 CPU를 제외하면 실행 자원이 부족할 수 있다. 실패는 `Infeasible`과 원인을 기록하고 Accepted나 κ=0으로 대체하지 않는다.

## 3. 최소안의 구체 측정 목록과 시간

**선택: Jetson 3대 × 각 2모드 × M/P/S = 18조합, CPU q1·batch1**. S는 `firstfloor-ort-cpu-int8-arm64`, M/P는 `firstfloor-ort-cpu-arm64`다. 최적 배치·CUDA 전체 비교는 아니지만 세 모델 모두에서 모드를 제어 대상으로 복원하는 최소 한 조건 비교다. runtime7 digest와 모델/전처리/데이터셋의 기존 고정값을 유지한다. o/x의 S CPU INT8은 q1 신규 profile이 필요하며 기존 q2/q4의 d나 loaded 비용을 복사하지 않는다.

시간은 모두 **[INFERENCE] 계획 추정**이며 실측 완료 시간·상한 보장이 아니다. `hack/measure/plan-e5a.py:21–33`의 기존 BEO(M CPU6분, P15분, Jetson S60분), `gen.py`의 d 대상당 최대3600초, loaded120초/unloaded300초를 근거로 한다. 저모드 B1의 미측정 지연에는 M/P 각2배까지, S60–120분을 둔다. d는 대상당15–60분, prepare/idle/remove는10–20분, 빈 Node 양방향 전환은 Node당15–30분의 계획 여유다. 실패 재측정·이미지 변경·U3/U4 개발 대기는 포함하지 않는다.

| 측정ID prefix | Node/모드 | profile M/P/S (분) | d M/P/S (분) | 준비·제거·loaded idle M/P/S (분) | 설명 |
|---|---|---|---|---|---|
| u2-o-maxn | jetsono/MAXN | 0/0/60–120 | 15–60/15–60/15–60 | 10–20/0/10–20 | M loaded 직접 연결 부족, S q1 다섯 근거 신규 |
| u2-o-15w | jetsono/MODE_15W | 6–12/15–30/60–120 | 각각15–60 | 각각10–20 | 세 모델 신규 |
| u2-x-15w6c | jetsonx/MODE_15W_6CORE | 0/0/60–120 | 각각15–60 | 10–20/0/10–20 | M loaded 부족, S q1 다섯 근거 신규 |
| u2-x-15w4c | jetsonx/MODE_15W_4CORE | 6–12/15–30/60–120 | 각각15–60 | 각각10–20 | 세 모델 신규 |
| u2-n-maxn | jetson-nano/MAXN | 0/0/0 | 15–60/15–60/0 | 10–20/0/0 | 기존 S q1, exact Accepted 확인 후 생략 |
| u2-n-5w | jetson-nano/5W | 6–12/15–30/60–120 | 각각15–60 | 각각10–20 | 빈 Node idle5분 추가 |

d를0으로 적은 nano/MAXN/S도 source의 새 규칙·runtime·모델 일치를 확인하지 못하면15–60분을 더한다. 다른 existing Accepted exact d가 확인되면 해당 행만 중복 측정을 생략한다. 기존 q1 BEO는 모든 배치를 유지하고 B1만 선택한다. 새 BEO는 `batchSizes: [1]`이다. 신규 profile **11조합**(저모드9, o/x 고모드 S 각1), d 최대17조합, loaded 비용 부족14조합, unloaded idle 부족1모드, 빈 Node 전환 부족2쌍4방향이다. 기존 o 쌍은 원시 참조·오차·반환 상태를 확인한 뒤 재사용한다.

| Node | 최소 잔여 시간(직렬 추정) | 합산 근거 |
|---|---:|---|
| jetsono | 약4.7–12.4시간 | profile141–282분 + d90–360 + 비용50–100; 기존 양방향 비용 재사용 |
| jetsonx | 약4.9–12.9시간 | 위와 같음 + 양방향15–30분 |
| jetson-nano | 약3.6–9.7시간 | profile81–162 + d75–300 + 비용40–80 + 빈 idle5 + 양방향15–30분 |
| 합계 | **약13.2–34.9시간** | 총793–2091분, 전역 직렬, 재시도 미포함. 최초 세 조합 실측 후 나머지 추정 갱신 |

이는 U1 잔여 d와 중복 계산하지 않기 위한 **U2 단독 보수적 계획 여유**다. U1이 먼저 채운 exact d는 차감한다. U4 공유는 같은 기종 genesis에만 적용하고 서로 다른 Jetson3기종 사이에 공유하지 않는다. 날짜만으로 E5 시작을 약속하지 않는다.

## 4. CampaignOps 실행 절차(profile → d → 비용)

### 4.1 착수 게이트와 기존 자산

1. 진행 중 D25를 완료·정상 반환하고 같은 Node에 다른 측정/Serving/action이 없는지, Ready, measurement taint, 예약·비Serving CPU, 메모리, 전력 시계열, token 만료를 fresh GET/CR로 확인한다. **전역 동시1건**, 변경자는 CampaignOps뿐이다. nano5W와 jetsono15W/jetsonx15W4C의 CPU 예산부터 확인한다. q1 affinity는 해당 모드 online CPU에서 예약/비Serving을 빼고 선택하며 원본 CPU3/5를 그대로 복사하지 않는다. nano5W에 여유 CPU가 없으면 세 모델 최소 범위 미달이라는 blocker를 Main에 알린다. 예약 임의 변경·κ0 대체·5W 제외는 금지한다.
2. 신규 외부 경로 `O/campaign/v2/u2-power-20261027/<fresh-run>/`을 쓴다. `CHILL_EXPERIMENT_CONFIG=O/campaign/v2/d-20261027/inputs/environment.json`이 CampaignOps가 확인한 현행 정본이다. `hack/README.md`의 measurement preparation과 같은 경로/namespace/context를 쓰며 API token은 출력하지 않는다.
3. 템플릿은 `O/campaign/v2/lanes/{jetsono,jetsonx,jetson-nano}.yaml`(`---` 구분 JSON). 기존 CR을 다시 apply하지 않는다. 신규 CR에 prefix표＋`-{m,p,s}-{beo,d,cost}`＋고유 run suffix를 붙이고 status/UID/resourceVersion/ownerReference를 복사하지 않는다. 원본명·SHA256·runtime7 참조와 새 이름 대응을 외부 manifest.json에 고정한다.

**읽기로 확인한 자원 위험:** 캡처에서 nano는 reserved CPU `[0]`과 `nonServing.cpuReserved=true,cpuCount=1`을 보고했다. 선언5W의 online CPU는 `[0,1]`이다. [INFERENCE] 같은 예약·비Serving 점유를 유지하면 독점 q1 여유는0일 수 있다. jetsono15W는 online4개−reserved2개−비Serving1개로 q1만 남는다. 따라서 nano5W는 단순한 시간 부족이 아니라 최소 범위의 실제 적격성 blocker 후보이며, 측정 CR이 실행됐다는 것과 Serving 후보가 적격이라는 것을 별도 판정한다.

**CampaignOps 추가 확인:** 현행 rev90(`6b2755ec`)에는 새 ≤5% 규칙이 아직 배포되지 않았다. §4.3의 새 조건은 해당 코드 배포·readback 뒤 실행한다. D25 `batch1-plc-3`은 idle0.141121%여도 `Insufficient/UnknownRequestExecution`이며 `NotSaturated` 문제가 아니다. 원시는 `O/campaign/v2/d-20261027/batch1-plc-3/jetsono-187-d25-occ-plc-cpu/raw.json`이다. 유휴 문턱 완화로 실행 증거 누락을 면제하지 않는다. nano5W q1 CPU도 운영 담당의 실측 판정 전이다.

### 4.2 프로파일 생성·측정

각 Node에서 고모드→저모드, 각 모드 M→P→S 순서다. 모드마다 다음 세 단계를 마친 뒤 다음 모드로 간다.

| Node/모드 | M 템플릿 | P 템플릿 | S INT8 템플릿 |
|---|---|---|---|
| jetsono/MAXN | jetsono-006-mnv2-cpu-q1-maxn | jetsono-014-plc-cpu-q1-maxn | jetsono-011-smolvlm-int8-q4-maxn에서 q1 신규 |
| jetsono/15W | jetsono-054-mnv2-cpu-q1-15w | jetsono-052-plc-cpu-q1-15w | 같은 Node MAXN INT8에서 mode15W/q1 신규 |
| jetsonx/15W6C | jetsonx-006-mnv2-cpu-q1-15w6c | jetsonx-011-plc-cpu-q1-15w6c | jetsonx-010-smolvlm-int8-q2-15w6c에서 q1 신규 |
| jetsonx/15W4C | jetsonx-038-mnv2-cpu-q1-15w4c | jetsonx-036-plc-cpu-q1-15w4c | jetsonx-035-smolvlm-int8-q2-15w4c에서 q1 신규 |
| nano/MAXN | jetson-nano-006-mnv2-cpu-q1-maxn | jetson-nano-012-plc-cpu-q1-maxn | jetson-nano-903-smolvlm-int8-q1-maxn(기존 catalog) |
| nano/5W | jetson-nano-028-mnv2-cpu-q1-5w | jetson-nano-026-plc-cpu-q1-5w | jetson-nano-024-smolvlm-int8-q1-5w |

위 템플릿은 원본 선언의 이름이며, 미보유 저모드 CR을 이미 생성·완료된 것으로 뜻하지 않는다. nano903은 `jetson-nano.after903.yaml` 또는 해당 기존 CR의 spec을 읽는다.

- `BatchEnergyOptimization`: lane의 같은 모델·CPU executionPath 템플릿을 JSON으로 읽고 `spec.state.candidate.powerMode`, `targetNode`, `cpuRequest: "1"`, `cpuQuota: "1"`, 실제 빈 `cpuAffinity`, `batchSizes: [1]`을 설정한다. S는 INT8 템플릿(o q4, x q2, nano q1)에서 **새 q1**을 만든다. M/P modelSpec/preprocessor/deviceClass/executionPath, trial3회, dataset, active/idle protocol과 고정 runtime 선언은 유지한다. mode/quota가 다른 새 측정이지 옛 profile 상속이 아니다. CR 구조: `config/samples/profiling/edge_v1alpha1_batchenergyoptimization.yaml`.
- 기존 q1/M/P/S가 정확히 일치하면 BEO 재생성 불필요. `Complete`뿐 아니라 admission과 result/raw/execution 참조를 회수한다. 새 B1도 최소3 trial의 근거를 갖춘다. Failed/Insufficient는 원시 로그를 보존하고 조건을 무단 확대하지 않고 중단·원인 보고한다.
- CampaignOps가 `kubectl --context <기존context> -n chill-system create -f <새CR.json>`으로 한 건씩 생성한다. controller가 `ProfilingRun`/Job을 생성하므로 Run을 별도로 만들지 않는다. `kubectl get beo <name> -o json`과 `GET /api/v1/scheduler`에서 새 profile ID/reference와 실제 CPU/runtime/batches를 회수한다. 저모드 통계를 고모드의 비례식으로 만들지 않는다.

### 4.3 d 생성·측정(profile 통계 확정 뒤)

- CR은 `DispatchOccupancyMeasurement`. 현행 생성 함수는 `hack/measure/gen.py`의 `derive(row, framed_bytes, pipeline)` → `occupancy(row, computed, provenance)`다. 새 profile의 row는 `O/campaign/v2/phase-a/measurement-generalization-20261023/d-method/d-inputs-and-calculations.json` 같은 모델 CPU행의 형태로 **새로 만든다**. `caseID`는 prefix＋모델, `BEO`/profileID/Node UID/runtimeImage/powerMode/inputIdentity.cpu/workload UID·generation·SLO는 fresh 참조다. B1 실측 durationNanos에서 mean/sample SD/min/max·sampleCount/trialCount와 trial별 run/raw/execution key·SHA256을 채운다. preparePlanningKnownSeconds는 해당 측정의 실제 준비 경계에서 얻는다. 원본 row 통계·CPU·식별자를 남기지 않는다. `legacy-floor-scenario.json`의 framed request 상한은 같은 입력 계약의 실제 크기 확인과 함께 사용하고 pipelineScenario/sources도 고정한다.
- `gen.py` CLI는 기존34행 고정 계획용이다. 그대로 실행한 뒤 새 모드 CR이라고 해석하지 않는다. `post-d2-25-20261025/prepare.py:43–79`가 함수 호출·근거 고정·새 이름 저장의 예다. 새 row/입력JSON을 외부 run 아래 두고 기존 함수의 계산 결과를 CR로 저장한다. 미지의 통계를 가짜로 채워 선제 submit하지 않는다.
- 조건: batch1, closed-loop, K는 현행 `min(64, floor((L−여유)/(B×service time)))`, `maximumIdleFraction: "0.05"`, `conditions.maximumIdleNanosPerRequest=1000000`. N·warmup·duration·L·drain·준비·setup·collection은 derive 결과(총3600초 이하)를 쓴다. 측정 lifetime에 E5 L을 기계 복사하지 않는다. supplyExtension을 포함한 현행 계산과 gateway cap을 대조한다.
- `kubectl create -f <새d.json>` 후 terminal CR·immutable raw·consumer GET을 보존한다. Accepted는 식별된 idle≤5%, 완전한 실행 증거, dRange/kappaErrorBound를 확인한다. signed d를 보존하고 consumer max(d,0)와 구별한다. source의 mode/runtime/Node UID/model을 맞춘 뒤 다음으로 간다. 원시 재판정만으로 새 Accepted를 만들지 않는다.

### 4.4 준비·제거·idle·모드 전환 비용

- CR은 **`OperationCostMeasurement`**, 현행 scheduler가 소비하는 operation 연속 측정을 쓴다. 템플릿: `lanes/jetsono.yaml`의 `jetsono-060-cost-15w`, `jetsonx.yaml`의 `jetsonx-043-cost-15w4c`, `jetson-nano.yaml`의 `jetson-nano-034-cost-5w`. 기존 전체 step을 실행하지 않고 측정할 새 BEO만 참조하도록 구성한다.
- 모드별 새 cost CR에서 `spec.node`를 고정하고 기존 모드→목표 모드 `power`(이미 같으면 생략)→`idle(name=unloaded,300초)`(누락 시)→모델별 `prepare(batchEnergyOptimization=<해당BEO>)`→`idle(120초)`→`remove(batchEnergyOptimization=<동일BEO>)` 순서다. §3의 부족14조합만 수행한다. 기존 M loaded 참조 부족4조합도 이 절차로 보완하며 prepare 비용 보유만으로 idle 완료라 하지 않는다. 불필요한 기존 variant/CUDA/power 목적지는 제거한다.
- observation/timing은 `gen.py:309–333` 현행 cost 생성과 같다(interval1000ms, requestTimeout800ms, maximumGap3000ms, maximumOperationSeconds2250, 원시 timing source). 옛 lane coverage0.9/경계 필드를 현행 계약에 무검증 복사하지 않는다. node-exclusive-wall scope, meter series/reference, signed 불확실성 S+G를 보존한다.
- 모든 모델 remove, load/outstanding/actions=0 확인 뒤 x의15W6C→15W4C→15W6C와 nano의MAXN→5W→MAXN을 power step으로 측정한다(같은 cost CR 후속 step 또는 새 CR). Node UID·from/to·방향별 시간/energy/uncertainty/raw 참조를 회수한다. o의 기존 MAXN↔15W는 의미·조건이 일치하면 재사용한다. 누락 방향을 반대 방향이나 경유 합계로 채우지 않는다.
- `ModelLoadedIdleBaseline` / `PowerModeTransitionCost` 전용 CR 샘플은 `config/samples/profiling/`, 설계는 `docs/design/profiling/plans/10-transition-cost-measurement.md`다. 전용 CR은 loaded 상태 Ready까지 포함하는 다른 계약이며 빈 Node operation cost와 대체하지 않는다. loaded 직접 전환을 추가 평가할 때만 두 모드 exact-state Complete baseline과 Serving readiness witness를 갖춘 뒤 전용 CR을 생성한다.

### 4.5 완료·반환·E5 연결

조합별 terminal CR/result/raw/consumer paths를 manifest에 추가하고 시작/끝UTC·소요초·모드·CPU·runtime·batch를 고정한다. 전역 직렬 빈 슬롯에서만 다음 CR을 생성한다. 마지막 owned Job/Pod·measurement taint·예약·잔여 request/action·Ready를 확인하고 정상 모드로 반환해 증거를 남긴다.

`GET /api/v1/scheduler` exact options/d/costs/baselines와 `GET /api/v1/scheduler/capacity` 실제 Built/eligible을 대조해 **18조합 모두의 다섯 근거** terminal/admission을 확인한다. 자율off의 capacity 빈 결과는 κ0 증명이 아니다. SLO/m 최종 선택 뒤 κ를 다시 얻고 Node별 eligible 최대값으로 R을 재계산하며 동시 혼합 자원도 판정한다. U7 E5-5에서 모드 후보가 실제 바뀌는지, 안 바뀌면 자원/κ/feasibility/에너지 차이의 실제 차단 원인을 보인다. 새 scope가 갖춰져도 E5 숫자1회 사용자 승인 전에는 연구 실행하지 않는다.

## 5. 선택지와 E5 일정 영향

아래 시간은 모두 **[INFERENCE]**다. 동시 실행을 가정해 임의로 단축하지 않는다.

| 안 | 측정 범위 | 추가 소요 | E5 영향 |
|---|---|---|---|
| **최소(실행)** | §3의18조합 B1/q1, 제거 뒤 모드 전환, 다섯 근거 | 약13.2–34.9시간, 기존 exact d 완료분 차감 | U1→최소범위 완료→κ/R→숫자 승인→E5. 당일 시작 보장 없음 |
| 중간(계획만) | 최소에 o CUDA M/P/S, x/nano CUDA M과 기존 CPU quota 변형·실측 batch목록을 선택 두 모드에 확장. genesis 신규 clock은 U3 후 CUDA M/P, genesis2→3 공유는 U4 확인 후 | 최소에 약20–50시간 추가 여유. Smol CUDA는 jetsono만, genesis Smol CUDA OOM 제외 | 최소안 E5 숫자 재사용 금지, candidate 집합/R 갱신. 수일 지연 예상 |
| 전체(계획만) | 대상8Node 모든 선언 mode, 모델별 지원 CPU/CUDA·전체 선언 quota/batch·각 exact 다섯 근거. Jetson15 Node-mode×3모델=45조합 이상. genesis 신규 선언은 U3 결과 반영 | CPU q1/B1만도 최소 외27조합×약1.5–4시간=40.5–108시간 추가. CUDA/quota/batch는 더 증가, 미지원 backend는 제외 사유 표시 | 최소 포함 약54–143시간 계획 여유이며 모든 변형은 더 길어짐. E5를 전체안 완료까지 대기시키지 않음 |

전체안에서 자유로운 모드 간 직접 전환을 허용하면 빈 Node 방향 수는 o4×3=12, x9×8=72, nano2×1=2로86방향이다. 현재 o4/x11/nano0이므로 부족71방향을 측정한다. 중간 모드 경유를 직접 비용으로 쓰지 않는다. loaded 직접 전환은 모델·실행 상태별 별도 추가이고86방향에 포함하지 않는다. 2CORE 자원 부적격/OOM/미지원 runtime은 실패·미지원으로 공개하며 모든 선언 모드에서 모든 모델이 동작한다고 약속하지 않는다.

U3 genesis 새 모드명·실증과 U4 동일기종 확인 뒤 genesis행을 갱신하되 Jetson 최소안은 이를 기다리지 않는다. U1 잔여 MNV CPU8은 U4 뒤라는 공통 순서를 유지한다. 이 문서는 최소안 외 자동 실행 승인이 아니다.

U3 현재 권고 단계는 `GPU_1500`, `GPU_1200`, `GPU_900`이다([decision-log](../intent/decision-log.md)). 이들은 이 표의 live GET 당시 선언되지 않았으며 세 모드 모두 M/P CUDA profile·새 규칙 d·준비/제거·idle·직접 전환 근거는 미확인으로 둔다. U3의 genesis2 1500↔900 실증과 U4의 실제 대상 확인이 끝나면 중간/전체안 genesis행을 그 참조로 갱신한다. RTX 이름은 같아도 기본 power limit/VBIOS가 달라 확인 측정 없이 공유하지 않는다.

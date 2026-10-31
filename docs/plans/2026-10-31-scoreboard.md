# 최적화 사이클 고정 점수판 — 2026-10-31

## 동결 계약
- `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy`, `C=O/campaign/v2/d-20261027/control-222-full`, `R=/home/genesis1/ETRI/chill`.
- 기준 서버: **d4a933bf033d87e90c2777cd9276526c317b73b2**(7회차). 기준점수: **최초 측정으로 채움**.
- 7회차 전체 행은 아래 미니 행과 길이·부하가 다르므로 실패 수·에너지를 기준점수에 대입하지 않는다. live b0877c1e도 기준 버전이 아니다.
- 행·순서: `E5-3m → E5-5m → E5-7m`; 정의는 `36c8778c:hack/e5/micro-stages.example.json`, `micro=true`, 정본 전체 행/정적 기준선과 분리한다.
- E5-3m: 180초 α=.5 → 300초 α=.9(480초); E5-5m: 180초 α=.3 → 300초 α=.9(480초).
- E5-7m: 360초 α=.3, 120초에 실제 양의 route/assignment가 있는 serving Pod를 UID 사전조건으로 1회 삭제; cordon/강제삭제 없음. 실제 선택·UTC·응답·회복 증거를 보존한다.
- rate basis는 **3회차** `C/binding-3.json`의 κ·payload·workloads·arrivals·seed. rate=α×고정 share×R, share MNV/PLC/Smol=.45/.10/.45; 새 κ로 부하를 재조정하지 않는다.
- Poisson seed: MNV(image-classification-tight-slo)=101, PLC(plc-anomaly)=202, Smol(smolvlm2-vqa)=303. 생성된 offsetsNS·arrivalSHA256도 같은 파일에서 고정한다.
- 실행 입력은 `C/binding-scoreboard.json`(binding-8 + 위 미니 schedule; 기존 e5.bind의 schedule 생성문 재사용). SHA256=`8a85d0908631408e9a5bff67e098889ff301598c38b3e471a669802e6a0e2611`.
- **설정 전체**는 그 파일의 `schedulerSettings`; h=`scaleDownHeadroomFraction=.25`. canonical JSON(sort_keys, compact) SHA256=`a6384805aa472b16a5d661c7967a6b9f16431f74daebecb53439b122dde84345`.
- 정책 설정은 고정하고 행별 autonomy/controls/excludedNodes는 원본 복원 계약을 따른다. payload·producer·SLO·timeboxes·energyCoverage도 binding에서 고정한다.

## 같은 시작 상태
- 원본은 `C/full-E5-{3,5,7}-223/{pre-state,pre-settings}.json`; 기대 fingerprint·입력 SHA256·복원 제외 항목 정본은 `C/scoreboard-inputs.json`의 `restore`다.
- E5-3-223은 실제 #222 원본 `C/full-E5-3/` 두 파일의 byte-identical 동결 복사다. `full-E5-3-223/provenance.json`에 원본 경로·SHA256을 보존하며 #223에서 측정했다고 주장하지 않는다.
- 기존 fingerprint의 served canonical instances(프로필/CPU·메모리/batch/wait), routing, native modes를 복원한다. physical ID 재결합만 허용하며 예약·owner-only 미서빙 준비는 기존 계약대로 제외한다.
- 각 행의 restore receipt `accepted=true`, `fingerprint==expectedFingerprint`와 immediate post-GO route 일치가 필수다. 다른 시작 상태 결과는 같은 점수판에 합치지 않는다.

## 점수와 가장 작은 계산법
- 점수 `(F,J)`를 **사전식 최소화**: F가 작으면 승리, F 동률일 때만 J가 작은 쪽이 승리한다.
- `F = Σ(3행×3워크로드 failures) + count(D1 == "FAIL")`; failures는 outcomes의 `error+timeout+preprocess_failed+delivery_failed` 합이다. D1 FAIL은 요청 수가 아니라 행×워크로드 판정 한 건당 1이다.
- `J = Σ(3행 power.wholePoolJoules)`; 전체 POOL을 행당 한 번만 합산한다. activePool·워크로드별 공유 전력·리플레이 전력을 더하지 않는다.
- 기존 `round.py`의 `tables-TAG.txt`에 failures/D1/whole POOL이 모두 있고 comparison JSON에는 원시 requests/power가 있다. **round.py/e5.py 변경·새 reducer·커밋은 불필요**하다.
- 아래는 동일 tables 원본 judgment 필드만 더하는 오프라인 명령이다(`C`를 위 절대 경로로 export, `TAG=10`; 후보는 해당 태그). 출력 반올림값 대신 원시 J를 쓴다.
```sh
TAG=10 python3 - <<'PY'
import importlib.util, json, os
from pathlib import Path
spec = importlib.util.spec_from_file_location('round_report', 'hack/e5/round.py')
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)
values = [r.sections(Path(os.environ['C']) / f'full-{row}-{os.environ["TAG"]}' / 'judgment.json') for row in ('E5-3m','E5-5m','E5-7m')]
assert all(v and len(v['requests']) == 3 for v in values), 'Incomplete rows'
metrics = [m for v in values for m in v['requests'].values()]
assert all(m.get('D1') in ('FAIL','OBSERVED_THRESHOLD_MET') and isinstance(m.get('outcomes'),dict) for m in metrics), 'Unknown requests'
assert all(v['power']['complete'] and v['power'].get('energyStatus','MEASURED') == 'MEASURED' and v['power']['wholePoolJoules'] is not None for v in values), 'Unknown energy'
print(json.dumps({'F':sum(sum(m['outcomes'].get(k,0) for k in ('error','timeout','preprocess_failed','delivery_failed')) + (m['D1']=='FAIL') for m in metrics), 'J':sum(v['power']['wholePoolJoules'] for v in values)}))
PY
```
- 미측정/UNKNOWN/INVALID_GENERATOR·복원/설정/배포 gate 실패·주입 무효는 점수 미확정이다. 누락을 0으로 대체하거나 성공 행만 합산하지 않는다.

## 리플레이와 측정 전 gate
- 입력은 `C/{full-E5-3-7,full-E5-5-7,full-E5-7-7,full-E5-5-8d}`의 scheduler/catalog/requests archive **4 stage**; 각 경로·SHA256은 `C/scoreboard-inputs.json`의 `replay.stages`에 동결했다.
- rows/profiles/scheduler는 `O/deployB/control-262-20261031/{kappa-rows.json,profiles-gate.json,scheduler.json}`; SHA256도 같은 manifest의 `replay.inputs`다. nextTicks=3, compare+guard를 기존 replay-gate.py로 실행한다.
- `--source d4a933bf --source 후보 --reference d4a933bf`; stage별 bad tick을 기록된 `(at, targetID)`로 대응하여 후보에만 있는 bad tick이 하나라도 있으면 FAIL. 다른 tick의 개선으로 상쇄하지 않으며 요약의 `newBadTicks` 목록·`fixedTicks` 수로 확인한다. 4개 중 하나라도 실패하면 배포/미니 측정을 하지 않는다. 리플레이는 F/J 실측 대체가 아니다.
- 측정 전 타 세션 배포 금지 방송·창 확보, restore 후 **첫 tick 전체 실제 settings(h 포함)**을 동결 정책/원본 topology와 대조하고 서버 source/digest·Ready·외부 deployment template 불변을 확인한다. 불일치 시 부하 시작 금지.
- 첫 load tick h/서버 restart·digest/외부 배포 감시 증거도 보존한다. round.py 기존 감시만으로 전체 settings의 사전 대조·방송이 자동 완료됐다고 간주하지 않는다.

## 기준 측정 선택과 실행 인계
- A **권고**: d4a933bf부터 같은 미니 행을 최초 측정 → 다음 라운드 b0877c1e 측정. B(live만 측정+기준은 리플레이만) 대비 기준용 **배포 1회+측정 1회 추가**; 이후 live 복귀 배포 1회도 필요하지만 F/J 기준을 실제로 확보한다.
- B: live 유지로 기준용 배포/측정 비용을 아끼지만 d4a933bf 미니 F/J가 없으므로 기준 대비 SLO·에너지 개선을 주장할 수 없다. replay PASS는 실측 동등성 증명이 아니다.
- `O/campaign/v2/rounds/round-10-baseline.json`: source=d4a933bf, harness=b0877c1e, tag=10, restoreOriginTag=223, compareTags=[](전체 길이 7회차와 잘못 비교하지 않음).
- baseline publication=`O/deployB/control-257-20261030/publication.json`, server digest=`sha256:1f1c201192946d75d8d01d19a455590f9ff311c2c69bb84124037964ed04fa04`(platform digest도 publication 사용). 다음 후보 publication=`O/deployB/control-268-20261031/publication.json`(b0877c1e); 서로 바꿔 쓰지 않는다.
- `C/release-round-10.json`은 release-round-9 형식으로 Main 승인 1회 측정·notBeforeUTC=2026-10-31T07:40:00Z를 기록한다. 이 시간 제한은 운영 조건이며 runner가 자동 강제하지 않는다.
- 실행은 Main이 창·gate를 확인한 뒤 `python3 hack/e5/round.py --round O/campaign/v2/rounds/round-10-baseline.json`(O는 절대 경로로 치환) **한 명령만**. 이 문서 작업에서는 배포·클러스터 변경·측정·체크 실행을 하지 않았다.

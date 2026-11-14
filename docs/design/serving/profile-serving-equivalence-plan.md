# 프로파일과 서빙의 차이 분석

[프로파일 측정 세션](profile-session.md)이 이 문서의 계획을 대체했다. 세션은 서빙의 준비 동작, Service 전달, 실행기 기록으로 프로파일을 측정하므로 프로파일과 서빙이 같은 실행 경로, 이미지, 운영점을 쓴다. 이 문서는 옛 프로파일과 서빙이 달랐던 곳의 분석만 남긴다.

근거:
- `ops/pools-deploy/intent/profile-spec-20261113/report.md`: serving–profile 괴리 집계. 요청 392,430건, 실행 배치 353,425개, serving profile 14개. 원자료는 `data/`.
- `ops/pools-deploy/intent/profile-spec-20261113/root/`: 단계별 분해. profile 원시 trial 111개의 `profile-raw/`, `stage-decomposition-report.txt`, `stage-comparisons.json`, `exact-input-matched-summary.jsonl`.
- [측정 명세](../../spec/00_measurement.md), [구성 선택 명세](../../spec/04_configuration_selection.md).

## 관측한 차이

스케줄러는 profile의 배치별 실행시간 분포와 에너지로 SLO, 용량과 에너지를 판단한다. 같은 조건에서 profile과 serving이 같은 능력을 보여야 한다. 분석 대상 배포에서는 다음 차이를 관측했다.

- genesis2 IC CUDA의 serving 실행시간은 profile의 1.8~3배다. jetsono PLC는 2.9배, jetson-nano PLC는 2.3배다.
- serving에 쓰인 profile 14개 전부가 측정과 다른 실행 이미지로 돌았다.
- genesis2 GPU_1500의 모델 적재 유휴 전력은 profile 118.4W, serving의 요청 없는 구간 69.4W다.
- 같은 조건의 profile과 serving 대조 단계가 없었다.

### 운영점에 따른 실행시간 차이

profile 원시 trial의 전처리, 추론, 후처리와 IPC 잔차를 serving 배치 기록과 대조했다(`root/stage-decomposition-report.txt`). 아래는 중앙값 비교다.

| 사례 | 차이가 나는 단계와 조건 | 같은 조건으로 좁힌 결과 |
|---|---|---|
| genesis2 IC CUDA 배치 2 | 전체 배치의 추론 p50 1.2ms → 24.7~25.8ms. 실행 중 다른 요청이 들어온 배치는 추론 25.8~27.7ms, service 39.9~50.1ms | 다른 요청이 안 들어온 배치는 service p50 13.5~15.1ms, 추론 p50 1.20~1.25ms. profile 13.97ms, 1.20ms와 중앙값이 같다. 이 배치들의 추론 p95는 28.7~31.3ms로 분포는 다르다 |
| genesis2 IC CUDA 배치 1 | 전처리는 평균 초과분의 57~73%, 추론은 23~40%. 직전 유휴 간격 20~100ms 배치는 전처리 22ms | 유휴 간격 5~20ms 배치는 service p50 8.9ms. profile 7.5ms에 가깝다 |
| jetson-nano PLC CUDA 배치 1 | 직전 유휴 1초 이상인 배치 420개의 추론 35.5ms | 유휴 1ms 미만은 추론 14.67ms, service 18.7ms. profile 14.64ms, 18.1ms와 중앙값이 같다 |
| jetsono PLC 배치 1 | 전처리, 추론과 잔차 모두 차이. 다른 요청이 안 들어온 배치도 3.4ms로 profile 1.18ms와 다름 | 실행 중 유입만으로 설명되지 않는다. 유휴 간격까지 맞춘 비교는 아직 없다 |
| lattepanda IC CPU, Smol | 차이 작음: 1.01~1.07배 | |

- 실행 중 유입과 긴 실행의 관계는 양방향이다. 실행이 길면 그동안 다른 요청이 들어올 확률도 높다. 완료 기록이 없는 유입은 집계에서 빠진다. “유입이 실행을 늦춘다”는 인과는 [INFERENCE]다.
- profile의 직전 유휴 간격은 측정 도구의 속도가 정한다. 배치와 노드별 중앙값은 1~59ms, 최대는 대부분 120ms 이하다. serving은 0.1ms부터 수 초까지다(`data/latency-with-conditions.json`).
- 분석 대상 profile은 요청 하나가 끝나면 다음 요청을 보내는 방식으로 측정됐다(`runtime/ortcpu/runner.py`).
- 프로파일링 및 평가 부분집합에 같은 JPEG 36개가 있고, 같은 이미지끼리 비교해도 genesis2 배치 1은 1.83~2.03배, lattepanda IC는 1.04배다(`root/exact-input-matched-summary.jsonl`). 당시 측정 이미지 안의 데이터가 현재 로컬 사본과 같다는 전제는 [INFERENCE]다.
- [INFERENCE] 유휴 뒤 클럭과 캐시 상태, 실행 중 수신 처리의 간섭이 원인 후보다.

### 실행 이미지

분석 대상 배포에서 측정 이미지는 profile 선택과 노드 간 재사용 비교에서 제외됐고, serving 준비가 측정 이미지를 선언 이미지로 덮어썼다. 명세는 같은 실행 경로와 버전을 요구한다. 분석 대상 측정 이미지의 요청 해석은 gateway가 보내는 `context.admissionPolicy`를 모르는 필드로 거절한다(HTTP 400).

### 유휴 전력

- 측정 profile 14개 중 13개의 유휴 측정은 시작 warmup 1회 뒤 2초 정착, 10초 측정이다(`data/conditions-final.json`).
- genesis2 IC CUDA의 유휴 118.4W와 serving의 요청 없는 2분 구간 69.4W, 표본 168개는 49W 다르다.

## 근원

| 근원 | 확정도 |
|---|---|
| profile 측정에 serving의 직전 유휴 간격 범위와 실행 중 유입이 없음 | 측정 조건 차이는 확정. 물리적 원인은 [INFERENCE] |
| 실행 이미지가 profile 적용 조건에서 빠져 미측정 이미지로 serving | 분석 대상 배포에서 확정 |
| profile과 serving의 유휴 전력이 다른 상태에서 측정됨 | 차이는 확정. 시점 원인은 [INFERENCE] |
| 같은 조건의 profile과 serving 대조가 없음 | 분석 대상 배포에서 확정 |
| dispatch occupancy가 배치 시작 간격에서 profile 평균을 차감 | 분석 대상 배포에서 확정. 조건부 service에 `B*d`를 더하는 재생에서 오차가 생기고 음수는 0으로 잘림 |

## 세션 측정이 다루는 범위

세션은 서빙과 같은 실행 경로와 이미지로 고정 배치 인스턴스를 띄워 포화, 중간, 저부하 수준의 서비스 기록(처리 시간과 직전 유휴 간격)을 모으고, 유휴 전력을 부하 뒤 정착한 구간에서 읽는다. 점유 시간은 포화 배치 쌍의 시작 간격에서 앞 배치의 실제 처리 시간을 뺀 값이다. 같은 조건의 profile과 serving 대조는 [프로파일 측정 세션](profile-session.md)의 운영 시범 측정이 한다.

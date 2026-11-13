# 운영 에이전트 가이드

## 운영 안전

- 현재 대상과 증거의 출처를 먼저 확인한다. 명령과 실패 원형을 보존하고 관측과 추론을 구분한다.
- 배포와 빌드는 `/tmp/chill-heavy.lock`의 배타 잠금을 공유한다. 점유 중이면 중단한다.
- 라이브 변경, 이미지 발행과 하드웨어 조작은 별도 운영 승인을 받은 담당자만 수행한다.
- 강제 삭제, finalizer 제거, 암묵적인 reset/rollback과 자동 재시도는 금지한다.
  실패하면 부분 상태와 증거를 검토하고 새 recipe 및 증거 디렉터리로 roll-forward한다.
- 인증정보와 민감한 운영 증거는 체크아웃 밖에 보관하고 공개 저장소에 올리지 않는다.
- 캠페인 동안 genesis1에서는 제품 구성요소만 실행한다.
  개발 빌드, 테스트와 에이전트 작업은 캠페인 밖에서 수행한다.
  제품 제어 비용을 포함하는 자원 풀 에너지에 비제품 작업을 섞지 않는다.

## Recipe 기반 roll-forward 배포

명령과 입력의 정본은
[배포 도구 안내](../../hack/README.md#recipe-driven-roll-forward-deployment)다.

```sh
hack/fast-deploy.sh run /absolute/private/recipe.json --dry-run
hack/fast-deploy.sh run /absolute/private/recipe.json --inspect --evidence /absolute/private/new-inspection
# 별도 승인 후 실제 배포 담당자가 실행한다.
hack/fast-deploy.sh run /absolute/private/recipe.json --execute --evidence /absolute/private/new-deployment
```

기본값과 `--dry-run`은 로컬 계획이다. `--inspect`는 지정한 클러스터의 자료를 읽는다.
이 두 모드는 이미지 발행이나 클러스터 변경을 하지 않는다.
선택한 validator/projector는 로컬 컴파일할 수 있다.
`--execute`만 발행과 변경을 허용하며 inspect와 execute는 새 `--evidence` 경로가 필요하다.

실제 source commit, kubeconfig, context, namespace, release, registry와 API 환경을 지정한다.
현재 `expectedRevision`, 필요한 `minFreeBytes`와 명시적인 image 선택을 검토한다.
과거 source나 승인 기록을 현재 승인으로 사용하지 않는다.
TLS CA, hostname과 Bearer 인증을 유지하며 token 원문을 argv와 로그에 남기지 않는다.
401/403과 실제 CLI 오류는 보존하고 중단한다.

배포 도구는 Helm user values, manifest, hooks, 보관 객체, 발행 receipt, 렌더 차이,
API 전후 응답, readiness와 실패를 private evidence에 보존한다.
source chart 차이와 staged 설정 차이는 각각 검토한 원문 파일로 승인한다.
Helm/chart가 CRD를 소유한다. 별도 SSA apply, ownership patch나 자동 keep 추가는 하지 않는다.

진행 중 측정에 대한 손실 방지 검사는 전체 namespace의 root와 소유 Job을 확인한다.
측정을 보존하는 제한된 image-only 경로의 자격은 배포 도구가 현재 자료로 판정한다.
그 외 변경은 미완료, 동결 전, 정리 대기 또는 삭제 중 작업이 있으면 중단한다.
운영자가 임의로 객체를 삭제하거나 검사 우회를 추가해서는 안 된다.

build, Helm upgrade와 readiness의 기본 예산은 각각 1800초, 900초, 480초다.
custom 명령은 양의 `seconds`를 지정한다. 시간 초과의 stdout/stderr도 보존한다.
Pod/API 준비 완료와 실험 판정은 별도로 보고한다.
프로파일 수와 측정 비교는 진단이며 실제 데이터 손실, 인증, 보안과 CLI 오류 검사는 유지한다.

builder 사용량을 빌드 전후 기록한다. 기본 prune은 꺼져 있다.
`pruneCompletedBuildCache: true`는 성공한 빌드 후 해당 CHILL builder의 미사용 cache 정리만 승인한다.
다른 builder, image, volume과 데이터 삭제는 이 옵션에 포함되지 않는다.
`hack/buildkitd.toml`은 20GB를 목표로 cache GC를 수행한다. 빌드 중 사용량은 이 목표를 넘을 수 있다.

## 측정과 평가 증거

CR 생성 직후 또는 생성 전 백그라운드에서 progress 수집을 시작한다:

```sh
python3 hack/measure/capture-progress.py \
  --node NODE --measurement CR_NAME --output-dir /tmp/NEW \
  --wait-for-pod 300 --capture-load
```

도구는 Workload 소유 Pod를 기다린 뒤 timestamped follow를 시작하고 `READY`를 출력한다.
원본 로그와 수집 실패를 보존한다.

평가 prepare는 동결한 입력의 도착 offsets를 한 번 준비한다.
producer에는 workspace PVC의 읽기 전용 파일을 전달한다.
실제 응답, 부분 결과와 exact-UID 소유 writer 정리 경계를 유지한다.
라이브 평가는 EvaluationRun/Stage의 Job 경계를 사용한다.
오프라인 도구는 [도구 안내](../../hack/README.md#offline-explicit-load-preview)를 따른다.

## 보존과 공간 정리

삭제는 소유자가 승인한 특정 경로에만 수행한다.
공간 부족 시 새 대용량 빌드와 측정을 보류하고 실제 사용량부터 확인한다.
나이, 크기 또는 cache라는 디렉터리 이름만으로 삭제하지 않는다.

| 종류 | 보존과 정리 조건 |
|---|---|
| 실험 및 배포 원본 | 요청/응답, 부하, scheduler와 전력 원시 행, progress, 설정, 출처, 실패, digest, 렌더 차이와 명령 로그를 보존한다. 독립 복제본과 원시 자료의 보고서 재생성을 확인하고 소유자가 보존 해제를 승인한 특정 디렉터리만 제거한다. |
| 모델, 데이터셋과 변환 입력 | 재취득 가능한 버전과 라이선스, 원본 hash 또는 독립 복제본을 확인하기 전에는 보존한다. 유일한 학습 및 보정 입력을 캐시로 간주하지 않는다. |
| 백업과 스냅샷 | 운영 필요 기간 종료, 독립 백업 확인, 증거 참조 없음과 소유자 승인을 모두 확인한다. |
| 재생성 가능한 언어 도구 cache | 실행 중인 build/test가 없는 창에서 잠금을 잡고 도구의 cache 정리 명령을 검토한다. module과 package 원본을 다시 받을 수 있어야 한다. 사용 중인 venv를 일괄 삭제하지 않는다. |
| Docker와 containerd | 저장 디렉터리 직접 삭제와 `docker system prune -a --volumes`는 금지한다. registry, DB와 실험 volume을 보존한다. 승인한 builder의 미사용 cache만 도구로 정리한다. |
| 임시 파일과 작업트리 산출물 | 프로세스, 열린 파일, worktree, 미커밋 파일과 증거 참조를 확인한다. 다른 연구자의 작업, `.git`, 유일 소스와 현재 에이전트 로그는 삭제하지 않는다. |
| 로그와 영속 데이터 | 사고 구간을 보관한 뒤 승인한 로그 보존 정책을 적용한다. kubelet volume, etcd, Ceph와 registry 데이터는 수동 삭제하지 않는다. |

정리 전후 같은 파일시스템의 `df -B1`, `df -i`와 대상 `du`를 기록한다.
`du`와 `df` 차이는 열린 파일, mount와 예약 블록 등을 확인한다.
표시된 reclaimable 크기 전체를 실제 회수 가능량으로 간주하지 않는다.

## 외부 서비스와 하드웨어

CloudCore 같은 외부 release는 해당 소유자의 현재 원본과 승인 절차를 따른다.
CHILL 배포 스크립트로 임의 변경하지 않는다. 디스크 압박이나 Pod Pending을
강제 배치, 임의 toleration 또는 직접 host 변경으로 우회하지 않는다.

노드 전원 조작 전에는 현재 장치 ID, 전력 계측과 실제 제어 경로를 대조한다.
제어 경로가 확인되지 않으면 중단한다. 승인된 동작의 전후 응답과 Node 상태를
외부 증거 디렉터리에 보존한다.

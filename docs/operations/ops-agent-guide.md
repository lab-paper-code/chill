# 운영 에이전트 가이드

- **증거 먼저:** 기존 `O=/home/genesis1/.cache/chill-assets/ops/pools-deploy` 기록과 라이브 값의 출처를 확인한다. 명령·실패 원형을 보존하며 관측과 추론을 구분한다.
- **공용 잠금:** 배포/빌드는 `/tmp/chill-heavy.lock`의 배타 잠금을 공유한다. 점유 중이면 중단한다. 다른 운영자의 작업과 겹쳐 실행하지 않는다.
- **시간 상한:** 명령은 제한 시간 안에 종료한다. upgrade는 빌드당 1800초, Helm 8분, 후속 Ready 확인 루프 전체 480초다. Pod 조회 timeout과 sleep도 남은 시간으로 제한하며 기한이 지난 결과는 성공 처리하지 않는다. 시간 초과의 stdout/stderr도 보존한다. 실패 시 증거 경로를 보고하고 멈춘다.
- **금지:** 강제 삭제, finalizer 제거, 자동 재시도, 암묵적인 reset/rollback 금지. 실패 후 재실행은 원인을 검토하고 별도 control 번호로 명시적으로 수행한다.

## 업그레이드 전용 배포

```sh
hack/fast-deploy.sh upgrade --source <commit> --control <NNN> --dry-run
hack/fast-deploy.sh upgrade --source <commit> --control <NNN>
# 범위를 명시하려면 --images server,profiler,dashboard (부분집합 허용)
```

`--dry-run`은 로컬 계획 출력만 하며 클러스터·이미지·증거 파일을 변경하지 않는다. 실제 실행에는 Python3/PyYAML, git, tar, curl, Docker buildx의 `chill-builder`, `/usr/local/bin/helm`, `/usr/bin/kubectl`이 필요하다. Docker는 기존 인증 설정을 사용한다. Helm과 kubectl 모두 `--kubeconfig /home/genesis1/.kube/config`와 `kubernetes-admin@kubernetes` context를 명시하므로 direnv의 개발용 `KUBECONFIG`에 의존하지 않는다. 대상은 `chill-system/chill`, 레지스트리는 `155.230.35.213:5000/chill`이다. control-178의 성공 방식대로 buildx output에 `registry.insecure=true`를 지정해 발행하고, manifest는 같은 레지스트리의 HTTP API로 조회해 digest를 확인한다.

소스는 `git archive`로 고정한다. auto는 라이브 Helm 이미지 태그 끝의 소스 SHA를 해석해 경로 차이를 비교한다. 해석 불가 시 추측하지 않고 중단한다. 제어 이미지 세 종류만 허용하며 runtime 7종은 빌드할 수 없다. 라이브 values 스냅샷 위에 변경 이미지 overlay만 적용한다. 소스 chart와 라이브 manifest가 다르거나 이미지 외 렌더 차이가 생기면 중단한다. registry digest → 렌더 객체 목록/차이 → server-side dry-run → upgrade → Ready/재시작0/digest 순으로 확인한다. 실행 imageID는 발행 index digest 또는 해당 linux/amd64 child digest와 일치해야 한다. 기존 옵션 전용 reset-deploy 경로는 변경하지 않는다.

증거는 `O/deployB/control-<NNN>-<UTC date>/`의 `commands.log`, `live-values.json`, `post-values.json`, `publication.json`, `render-diff.{json,txt}`, `server-dry-run.log`, `readiness.json`에 남고 성공하면 `O/deploy-log.md`에 한 줄 추가한다. 실패는 `failure.json`을 보존한다. **보고는 결론 한 줄 + 증거 절대 경로**로 끝낸다. 배포 명령 성공만으로 업무 시나리오의 성공을 주장하지 않는다.

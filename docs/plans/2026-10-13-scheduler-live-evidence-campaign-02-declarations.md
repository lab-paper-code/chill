# CHILL 캠페인 — 실행 선언과 설정 지속성 결과

- 상태: completed. native ARM64 child image 선언과 상시 `mobilenet-v2-050-firstfloor` 등록, production resolver `resolves=true`를 확인했다.
- Helm facts schema v1alpha7과 native 실행 정책을 values에 보존했다. live-only 수정이 다음 배포에서 되돌아간 것이 원인이었다.
- 근거: `873b6a0` (`fix(helm): align profiler facts with native execution declarations`), release 13·14.
- preflight/선언은 qualifying 측정이나 제어 성공이 아니다. [정식 측정 결과](2026-10-13-scheduler-live-evidence-campaign-03-profiling.md)와 구분한다.

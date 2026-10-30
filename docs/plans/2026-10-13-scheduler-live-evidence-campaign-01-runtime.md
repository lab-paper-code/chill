# CHILL 캠페인 — ARM64 실행 이미지 결과

- 상태: completed(2026-10-13 smoke). rasp5의 firstfloor ORT CPU 실제 추론과 K=1·2 각 3회 실행을 확인했다.
- ARM64 child manifest: `sha256:7b919a87721d2d0ff30da488a8b9ee215252c63b49facda1faaae32784b6da9a`, ORT 1.27.0 / NumPy 2.2.6 / CPUExecutionProvider / synthetic=false.
- 자원: CPU 1 / 512Mi, affinity [3]. affinity는 OS 독점 CPU의 증거가 아니다.
- `/tmp/chill-rasp5-firstfloor-unit1-*`는 임시 smoke 원시이며 qualifying profiling evidence가 아니다. 전력·에너지·SLO·scheduler 통과 주장은 없다.
- 정식 후속 결과: [프로파일링](2026-10-13-scheduler-live-evidence-campaign-03-profiling.md), 현재 평가는 [E5 보고서](../reports/e5-evaluation-20261030.md).

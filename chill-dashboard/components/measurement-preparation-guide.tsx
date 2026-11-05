import Link from "next/link"
import { measurementPreparationSource, measurementRequestExample } from "@/lib/measurement-guidance"

export function MeasurementPreparationGuide() {
  return <section aria-label="측정 준비 안내" className="grid min-w-0 gap-3 text-sm [overflow-wrap:anywhere]">
    <h2 className="font-medium">측정 전에 준비하세요</h2>
    <p className="max-w-prose text-muted-foreground">등록한 서비스(Workload)와 실행 장비를 확인하고, 이전 성공 요청의 측정 방식을 재사용하거나 담당자에게 측정 조건을 받으세요. 파일을 읽거나 검토하는 것만으로 측정을 시작하지 않아요.</p>
    <nav aria-label="측정 준비 자료" className="flex flex-wrap gap-x-5 gap-y-1">
      <Link className="inline-flex min-h-11 items-center text-primary underline underline-offset-4" href="/settings?tab=models">모델·처리 모듈·데이터셋 등록</Link>
      <Link className="inline-flex min-h-11 items-center text-primary underline underline-offset-4" href="/workloads">서비스 연결 확인</Link>
      <Link className="inline-flex min-h-11 items-center text-primary underline underline-offset-4" href="/measurements/requests">이전 측정 요청</Link>
    </nav>
    <details>
      <summary className="min-h-11 cursor-pointer py-2 font-medium">준비물과 담당자</summary>
      <ul className="grid list-disc gap-2 pl-5 pt-2">
        <li>운영자: 등록된 Workload가 실제 모델·처리 모듈·데이터셋을 연결하는지 확인하세요. 폼에서는 실제 등록된 서비스를 고르고, 문서에서는 그 이름과 연결 대상을 적어요.</li>
        <li>플랫폼 관리자: DeviceClass·Node·실행 경로·전력 모드를 등록하고, 장비 가용성과 접근 권한을 준비해요.</li>
        <li>측정 담당자: 같은 조건의 성공한 BatchEnergyOptimization(BEO) 요청을 확인하거나 예열·실행·유휴·전력 수집 조건을 제공해요. 첫 측정에는 담당자가 정한 조건을 사용하세요. 비밀번호·토큰·Secret 내용은 문서에 넣지 마세요.</li>
      </ul>
    </details>
    <details>
      <summary className="min-h-11 cursor-pointer py-2 font-medium">입력 방법과 측정 시간 읽기</summary>
      <div className="grid gap-3 pt-2">
        <p>직접 작성은 등록된 선택지와 이전 측정 방식을 이용해요. YAML/JSON 가져오기는 담당자가 준비한 네이티브 BEO 문서를 읽어요. 문서의 spec은 제공한 그대로 서버에 전달하며, 브라우저가 누락된 측정 숫자를 채우지 않아요.</p>
        <p><code>activeProtocol.durationSeconds</code>는 부하를 실행하며 측정하는 시간이고, <code>idleProtocol.durationSeconds</code>는 유휴 측정 시간이에요. 유휴 측정 전 안정 대기는 <code>settlingDurationSeconds</code>로 구분해요.</p>
        <p><code>durationToleranceSeconds</code>는 실행·유휴 시간의 허용 오차예요. 전체 소요 시간은 자동 탐색 범위와 실행 상태에 따라 달라져요.</p>
        <p>각 <code>metricObservation.durationSeconds</code>는 전력 기록 구간이에요. 실행·유휴 측정 시간과 별도 설정이며 해당 측정 구간을 포함해야 해요. <code>intervalMilliseconds</code>는 기록 간격, <code>timeoutMilliseconds</code>는 수집 요청 대기 한도예요. <code>warmupIterations</code>는 초가 아닌 예열 횟수예요.</p>
        <p>문서 검토는 로컬 읽기와 내용 확인이에요. 그다음 명시적으로 서버 검사(dryRun·preflight)를 요청해 접수 조건과 실행 계획을 확인하세요. 검사 성공도 측정 완료가 아니에요. 최종 제출에서만 실제 장비를 사용하는 측정을 요청하며, 접수 뒤 요청 목록에서 진행과 결과를 확인해요.</p>
      </div>
    </details>
    <details>
      <summary className="min-h-11 cursor-pointer py-2 font-medium">네이티브 BEO YAML 구조 예시</summary>
      <div className="grid min-w-0 gap-3 pt-2">
        <p>구조 예시예요. 모든 <code>{"<...>"}</code>를 실제 값으로 바꾸고, 숫자는 따옴표까지 지워 적으세요. namespace는 현재 선택값에 맞추세요.</p>
        <p>배치는 K=1, 2, 4, ... 순서로 artifact·runtime·예산 경계까지 자동 탐색해요. 운영자가 배치 목록을 입력하지 않아요. 이전 고정 배치 문서는 <code>spec.batchSizes</code>를 제거하세요. 지원 조건과 연결 관계는 서버 검사에서 확인해요.</p>
        <pre className="max-h-96 min-w-0 overflow-auto rounded-md border p-3 text-xs"><code>{measurementRequestExample}</code></pre>
        <p className="text-muted-foreground">YAML/JSON 가져오기에서 빈 입력란에 예시를 넣을 수 있어요. 작성 중인 초안은 덮어쓰지 않아요.</p>
        <p>파일·직접 입력을 분리한 실제 구현: <a className="inline-flex min-h-11 items-center text-primary underline underline-offset-4" href={measurementPreparationSource.href} target="_blank" rel="noopener noreferrer">{measurementPreparationSource.label} (새 창)</a></p>
      </div>
    </details>
  </section>
}

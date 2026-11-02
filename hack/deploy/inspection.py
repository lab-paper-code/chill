"""Read-only source-shared projection over explicitly captured assessment inputs."""
import json
import os
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def project(recipe, source, out, run, profiles, coverage, requests):
    out, source = Path(out), Path(source)
    cells = out / 'inspection-cells.json'
    cells.write_bytes(Path(recipe['inspectionCells']).read_bytes())
    adapter = out / 'inspection-adapter.go'
    adapter.write_bytes((ROOT / 'hack/deploy/inspect/main.go').read_bytes())
    assessed = out / 'inspection-assessed-requests.json'
    assessed.write_bytes(Path(recipe['inspectionRequests']).read_bytes())
    overlay = out / 'inspection-overlay.json'
    overlay.write_text(json.dumps({'Replace': {str(source / 'hack/deploy/inspect/main.go'): str(adapter)}}))
    environment = dict(os.environ, GOPROXY='off', GOSUMDB='off', GOTOOLCHAIN='local', GOWORK='off', GOFLAGS='')
    raw = run(['go', 'run', '-overlay=' + str(overlay), './hack/deploy/inspect',
               '--cells', str(cells), '--requests', str(requests), '--profiles', str(profiles), '--coverage', str(coverage),
               '--assessed-requests', str(assessed)],
              'coverage-projection.json', timeout=120, cwd=source, env=environment)
    report = json.loads(raw)
    report.update(applicationSource=recipe['source'], assessedVariantInput=recipe['inspectionCells'],
                  nativeMeasurementInput=str(requests), profileInput=str(profiles), coverageInput=str(coverage))
    (out / 'coverage-projection.json').write_text(json.dumps(report, indent=2) + '\n')
    return report

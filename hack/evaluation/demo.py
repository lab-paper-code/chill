#!/usr/bin/env python3
"""Preview explicit absolute load offline; execute experiments through EvaluationRun.

No deployment, registration, feature mutation or load is performed here. The
controller owns stage order, factor/stage/judge/cleanup Jobs and publication.
Use --config with demo-config.example.json and --out for a binding-v3 preview.
"""
import argparse
from pathlib import Path
import tempfile

import conductor
import evidence_io


def preview(config, destination):
    if destination.exists():
        raise ValueError('No preview overwrite')
    binding = conductor.bind(config)
    evidence_io.save(destination, binding)
    return binding


def self_check():
    with tempfile.TemporaryDirectory(prefix='evaluation-demo-') as directory:
        source = Path(__file__).with_name('demo-config.example.json')
        value = preview(source, Path(directory) / 'binding.json')
        assert value['schema'] == 'e5-binding.v4'
        assert value['schedule'][0]['seconds'] == 900
        assert value['schedule'][0]['parts'][0]['rates']['plc-anomaly'] == 11.620962232575499
        assert value['schedule'][0]['traces']['plc-anomaly']['seed'] == 202
        assert value['schedule'][0]['traces']['plc-anomaly']['offered'] > 0
    print('PASS offline absolute-load preview; no cluster calls or inference')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--self-check', action='store_true')
    parser.add_argument('--config', type=Path)
    parser.add_argument('--out', type=Path)
    args = parser.parse_args()
    if args.self_check:
        self_check()
    else:
        if not args.config or not args.out:
            parser.error('--config and --out required')
        binding = preview(args.config, args.out)
        print('BOUND_NOT_MEASURED: ' + ', '.join(row['id'] for row in binding['schedule']))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())

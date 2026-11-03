"""Strict frozen-evidence JSON, timestamps, source references and module loading."""
import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path
import re

_STAMP = re.compile(r'^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$')
_EPOCH = dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc)


def timestamp_ns(value):
    """Parse RFC3339Nano without rounding boundary decisions to float seconds."""
    match = _STAMP.fullmatch(value) if isinstance(value, str) else None
    if not match:
        raise ValueError('missing/invalid RFC3339Nano timestamp')
    base, fraction, zone = match.groups()
    instant = dt.datetime.fromisoformat(base + ('+00:00' if zone == 'Z' else zone))
    delta = instant.astimezone(dt.timezone.utc) - _EPOCH
    return (delta.days * 86400 + delta.seconds) * 1000000000 + int((fraction or '').ljust(9, '0'))


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def load(path):
    return json.loads(Path(path).read_text(), parse_constant=lambda value: (_ for _ in ()).throw(ValueError('Nonfinite JSON: ' + value)))


def save(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + '.tmp')
    temporary.write_text(json.dumps(value, indent=2, allow_nan=False) + '\n')
    temporary.replace(path)


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def ref(path):
    path = Path(path)
    return {'path': str(path), 'bytes': path.stat().st_size, 'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}


def load_binding(path):
    value = load(path)
    if value.get('schema') != 'e5-binding.v4':
        raise ValueError('FrozenInputInvalid: binding v4 required')
    return value



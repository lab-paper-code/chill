"""Strict frozen-evidence JSON, timestamps, source references and module loading."""
import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path


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



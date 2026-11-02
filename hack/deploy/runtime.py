"""Stage exact-image declarations using the source archive's existing inspector."""
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

import yaml


RUNTIME_IMAGES = {
    "CPU_AMD64_IMAGE": ("firstfloor", "linux/amd64"),
    "CPU_ARM64_IMAGE": ("firstfloor", "linux/arm64"),
    "CUDA_AMD64_IMAGE": ("firstfloor-cuda", "linux/amd64"),
    "CUDA_L4T_R32_IMAGE": ("firstfloor-l4t-r32", "linux/arm64"),
    "CUDA_L4T_R35_IMAGE": ("firstfloor-l4t-r35", "linux/arm64"),
    "CUDA_L4T_R36_IMAGE": ("firstfloor-l4t-r36", "linux/arm64"),
    "RKNN_IMAGE": ("firstfloor-rknn", "linux/arm64"),
}


def _references(publication):
    references = {}
    for variable, (role, platform) in RUNTIME_IMAGES.items():
        entry = publication.get("roles", {}).get(role)
        if not isinstance(entry, dict):
            raise ValueError("runtime staging requires complete six-role publication: " + role)
        image = entry.get("image", "").split("@", 1)[0]
        repository = image.rsplit(":", 1)[0]
        matching = [r for r in entry.get("platformReceipts", []) if r.get("platform") == platform]
        if len(matching) != 1:
            raise ValueError("runtime staging requires one actual platform receipt: " + role + "/" + platform)
        receipt = matching[0]
        child_digest = receipt.get("digest", "")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", child_digest) or child_digest not in entry.get("platformDigests", []):
            raise ValueError("runtime platform receipt digest mismatch: " + role)
        if receipt.get("revision") != publication.get("sourceSHA"):
            raise ValueError("runtime platform receipt revision mismatch: " + role)
        references[variable] = repository + "@" + child_digest
    return references


def _argument(argv, flags):
    found = []
    for index, arg in enumerate(argv):
        if arg in flags and index + 1 < len(argv):
            found.append(argv[index + 1])
        for flag in flags:
            if arg.startswith(flag + "="):
                found.append(arg[len(flag) + 1:])
    if len(found) != 1 or not found[0]:
        raise ValueError("runtime staging requires one explicit " + flags[0])
    return found[0]


def _inspector(source):
    path = Path(source).resolve() / "hack/runtime-declarations.py"
    spec = importlib.util.spec_from_file_location("chill_runtime_declaration_inspector", str(path))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return path, module


def _validate(raw, inspector, references):
    document = yaml.safe_load(raw)
    if not isinstance(document, dict) or set(document) != {"profiling"} or not isinstance(document["profiling"], dict) or set(document["profiling"]) != {"executionDeclarations"}:
        raise ValueError("runtime inspector must output only profiling.executionDeclarations")
    declarations = document["profiling"]["executionDeclarations"]
    expected = set()
    for device_class, architecture, cuda_variable, cuda_nodes, _ in inspector.CLASSES:
        cpu_variable, _ = inspector.CPU[architecture]
        expected.add((device_class, "", "ort-cpu", architecture, references[cpu_variable]))
        if cuda_variable:
            for node in cuda_nodes:
                expected.add((device_class, node, "ort-cuda", architecture, references[cuda_variable]))
    expected.add((inspector.RKNN_CLASS, "", "rknnlite", "arm64", references["RKNN_IMAGE"]))
    if len(expected) != 15 or not isinstance(declarations, list) or len(declarations) != len(expected):
        raise ValueError("runtime inspector must provide all 15 source-defined declarations")
    found = set()
    for declaration in declarations:
        if not isinstance(declaration, dict) or not isinstance(declaration.get("runtime"), dict):
            raise ValueError("malformed runtime declaration")
        payload = json.loads(declaration["runtime"].get("payload", ""))
        runtime = payload.get("runtime", {})
        backends = runtime.get("backends", [])
        if payload.get("schemaVersion") != inspector.SCHEMA or payload.get("verification") != inspector.VERIFICATION or runtime.get("family") != "firstfloor" or len(backends) != 1:
            raise ValueError("runtime inspector declaration provenance mismatch")
        identity = (declaration.get("deviceClass"), declaration.get("node", ""), backends[0], payload.get("architecture"), payload.get("image"))
        if identity not in expected or identity in found:
            raise ValueError("runtime inspector emitted foreign or duplicate image/declaration")
        packages = payload.get("packages")
        if not isinstance(packages, list) or not packages or any(not isinstance(p, dict) or not p.get("name") or not p.get("version") for p in packages):
            raise ValueError("runtime inspector omitted observed package versions")
        if backends[0] == "ort-cuda" and not declaration.get("access", {}).get("gpuUUID"):
            raise ValueError("runtime inspector omitted observed GPU UUID")
        if backends[0] == "rknnlite" and not declaration.get("host", {}).get("driver", {}).get("version"):
            raise ValueError("runtime inspector omitted observed RKNN driver version")
        found.add(identity)
    if found != expected:
        raise ValueError("runtime inspector declaration coverage mismatch")


def _wrapper(out, kube):
    """Record an exact Pod identity before create, even if create is interrupted."""
    owned = out / "runtime-owned-pods.txt"
    owned.touch(mode=0o600, exist_ok=False)
    wrapper = out / "runtime-kubectl"
    wrapper.write_text(
        "#!" + sys.executable + "\n"
        "import json, os, subprocess, sys, uuid\n"
        "KUBE = " + repr(kube) + "\n"
        "OWNED = " + repr(str(owned)) + "\n"
        "args = sys.argv[1:]\n"
        "command = KUBE + ['--request-timeout=120s'] + args\n"
        "if len(args) > 2 and args[0] == '-n' and args[2] == 'create':\n"
        "    pod = json.load(sys.stdin)\n"
        "    if pod.get('kind') != 'Pod':\n"
        "        raise SystemExit('runtime inspector may create only tracked Pods')\n"
        "    name = 'chill-runtime-inspect-' + uuid.uuid4().hex\n"
        "    pod['metadata'].pop('generateName', None)\n"
        "    pod['metadata']['name'] = name\n"
        "    with open(OWNED, 'a') as stream:\n"
        "        stream.write(name + '\\n')\n"
        "    result = subprocess.run(command, input=json.dumps(pod).encode())\n"
        "    raise SystemExit(result.returncode)\n"
        "os.execvp(command[0], command)\n"
    )
    wrapper.chmod(0o700)
    return wrapper, owned


def stage(publication, source, out, kube, run, timeout=3600):
    """Execute only in the parent's explicit live staging phase, before final render."""
    if isinstance(timeout, bool) or not isinstance(timeout, int) or timeout <= 0:
        raise ValueError("runtime inspection timeout must be a positive integer")
    references = _references(publication)
    kube = [str(arg) for arg in kube]
    if not kube:
        raise ValueError("runtime staging requires an explicit kubectl command")
    _argument(kube, ("--kubeconfig",))
    _argument(kube, ("--context",))
    namespace = _argument(kube, ("--namespace", "-n"))
    out = Path(out).resolve()
    wrapper, owned = _wrapper(out, kube)
    script, inspector = _inspector(source)
    environment = dict(os.environ, **references)
    environment.update({"KUBECTL": str(wrapper), "NAMESPACE": namespace, "INSPECT_TIMEOUT_SECONDS": str(timeout)})
    try:
        raw = run([sys.executable, str(script)], "runtime-inspection.yaml", timeout=timeout, env=environment)
        _validate(raw, inspector, references)
    finally:
        # Parent survives its bounded child-process TERM/KILL and owns this cleanup.
        names = sorted(set(owned.read_text().splitlines()))
        if any(not re.fullmatch(r"chill-runtime-inspect-[0-9a-f]{32}", name) for name in names):
            raise ValueError("invalid runtime invocation Pod ownership record")
        if names:
            run(kube + ["--request-timeout=20s", "delete", "pod", *names,
                        "--ignore-not-found=true", "--wait=false"],
                "runtime-inspection-cleanup.log", timeout=30)
    output = out / "runtime-values.yaml"
    output.write_bytes(raw)
    return output

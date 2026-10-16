#!/usr/bin/env python3
"""Inspect pushed firstfloor runtime images on testbed Nodes and print the
profiling.executionDeclarations values block for every DeviceClass.

Each image runs once as a probe Pod on a Node of the target class, under the
same identity and access the measurement Job uses. The payload is built only
from what the probe reads inside that exact image, so the result is
exact-image inspection output, not a restated claim.

Environment (runnable per-platform child references, REPOSITORY@sha256:DIGEST):
  CPU_ARM64_IMAGE, CPU_AMD64_IMAGE  required
  CUDA_AMD64_IMAGE, CUDA_L4T_R32_IMAGE, CUDA_L4T_R35_IMAGE, CUDA_L4T_R36_IMAGE,
  RKNN_IMAGE                        optional; an unset one omits its
                                    declarations and says so on stderr
  NAMESPACE (chill-system), KUBECTL (kubectl), INSPECT_TIMEOUT_SECONDS (3600)

Usage: hack/runtime-declarations.py > /tmp/runtime-declarations.yaml
       hack/runtime-declarations.py --self-check
"""

import json
import os
import re
import subprocess
import sys
import time

SCHEMA = "chill.dacs.io/runtime-declaration.v1alpha2"
VERIFICATION = "exact-image-runtime-inspection-v1"
IMAGE = re.compile(r"^[^@\s]+@sha256:[0-9a-f]{64}$")
MACHINES = {"x86_64": "amd64", "aarch64": "arm64"}
PROVIDERS = {"ort-cpu": "CPUExecutionProvider", "ort-cuda": "CUDAExecutionProvider"}
VIDEO_GID = 44

# CPU images are inspected once per architecture on a CPU-only Node.
CPU = {"arm64": ("CPU_ARM64_IMAGE", "rasp5"), "amd64": ("CPU_AMD64_IMAGE", "lattepanda")}
# DeviceClass, architecture, CUDA image variable, CUDA Nodes, CUDA groups.
# The GPU UUID binds a CUDA declaration to the Node it was read on, so every GPU
# Node of a class is probed and gets its own declaration.
CLASSES = [
    ("raspberry-pi-5-8g", "arm64", None, (), []),
    ("orangepi-5-plus-16g", "arm64", None, (), []),
    ("jetson-nano-4g", "arm64", "CUDA_L4T_R32_IMAGE", ("jetson-nano",), [VIDEO_GID]),
    ("jetson-agx-orin-64g", "arm64", "CUDA_L4T_R35_IMAGE", ("jetsono",), [VIDEO_GID]),
    ("jetson-xavier-nx-8g", "arm64", "CUDA_L4T_R35_IMAGE", ("jetsonx",), [VIDEO_GID]),
    ("jetson-orin-nano-8g", "arm64", "CUDA_L4T_R36_IMAGE", ("orin-nano",), [VIDEO_GID]),
    ("lattepanda-3-delta-8g", "amd64", None, (), []),
    ("edge-server-rtx-2080-ti-64g", "amd64", "CUDA_AMD64_IMAGE", ("genesis2", "genesis3"), []),
]
RKNN_CLASS, RKNN_NODE = "orangepi-5-plus-16g", "orangepi"
# The RKNN host boundary is the Orange Pi installation; the probe reads the
# driver version and platform token from the Node itself.
RKNN_HOST = {
    "libraries": [{"name": "librknnrt", "hostPath": "/usr/lib/librknnrt.so",
                   "containerPath": "/usr/lib/librknnrt.so"}],
    "devices": [{"path": p} for p in ("/dev/dri/card0", "/dev/dri/card1",
                                      "/dev/dri/renderD128", "/dev/dri/renderD129")],
    "allocation": {"resourceName": "", "exclusivityClaimed": False},
}
RKNN_ACCESS = {"privileged": True, "supplementalGroups": [VIDEO_GID, 110]}

PROBE = r"""
import json, platform, sys
from importlib import metadata
mode = sys.argv[1]
def version(name):
    try:
        return metadata.version(name)
    except metadata.PackageNotFoundError:
        return None
facts = {"machine": platform.machine(),
         "packages": {n: version(n) for n in ("onnxruntime", "onnxruntime-gpu", "numpy", "rknn-toolkit-lite2")}}
if mode != "rknnlite":
    import onnxruntime
    facts.update(ortVersion=onnxruntime.__version__, providers=onnxruntime.get_available_providers())
if mode == "ort-cuda":
    import ctypes, uuid
    cu, count = ctypes.CDLL("libcuda.so.1"), ctypes.c_int()
    if cu.cuInit(0) or cu.cuDeviceGetCount(ctypes.byref(count)):
        raise SystemExit("CUDA driver initialization failed")
    facts["gpuUUIDs"] = []
    for i in range(count.value):
        device, raw = ctypes.c_int(), ctypes.create_string_buffer(16)
        if cu.cuDeviceGet(ctypes.byref(device), i) or cu.cuDeviceGetUuid(raw, device):
            raise SystemExit("CUDA device UUID query failed")
        facts["gpuUUIDs"].append("GPU-" + str(uuid.UUID(bytes=raw.raw)))
if mode == "rknnlite":
    facts["driverVersion"] = open("/sys/module/rknpu/version").read().strip()
    facts["compatible"] = [t for t in open("/proc/device-tree/compatible", "rb").read().decode().split("\0") if t]
open("/dev/termination-log", "w").write(json.dumps(facts))
"""


def fail(message):
    sys.exit(f"runtime declarations: {message}")


def payload(image, architecture, backend, facts):
    """Build the runtime declaration from probe facts read inside the image."""
    if not IMAGE.fullmatch(image):
        fail(f"{image}: REPOSITORY@sha256:DIGEST required")
    if MACHINES.get(facts["machine"]) != architecture:
        fail(f"{image}: probe machine {facts['machine']!r} is not {architecture}")
    found = facts["packages"]
    if backend == "rknnlite":
        names = ["rknn-toolkit-lite2"]
    else:
        ort = "onnxruntime-gpu" if backend == "ort-cuda" else "onnxruntime"
        if found[ort] != facts["ortVersion"]:
            fail(f"{image}: {ort} {found[ort]!r} is not the imported onnxruntime {facts['ortVersion']!r}")
        if PROVIDERS[backend] not in facts["providers"]:
            fail(f"{image}: {PROVIDERS[backend]} is unavailable")
        names = [ort, "numpy"]
    for name in names:
        if not found[name]:
            fail(f"{image}: package {name} is absent")
    return {"schemaVersion": SCHEMA, "image": image, "architecture": architecture,
            "runtime": {"family": "firstfloor", "backends": [backend]},
            "packages": [{"name": n, "version": found[n]} for n in names], "verification": VERIFICATION}


def kubectl(*args, stdin=None):
    command = [os.environ.get("KUBECTL", "kubectl"), "-n", os.environ.get("NAMESPACE", "chill-system"), *args]
    result = subprocess.run(command, input=stdin, capture_output=True, text=True)
    if result.returncode:
        fail(f"{' '.join(command[:4])}: {result.stderr.strip()}")
    return result.stdout


def inspect(image, node, backend, access):
    """Run the probe in the exact image on node under the Job's identity and access."""
    container = {
        "name": "probe", "image": image, "imagePullPolicy": "IfNotPresent",
        "command": ["python3", "-c", PROBE, backend],
        "terminationMessagePolicy": "FallbackToLogsOnError",
        "securityContext": {"privileged": access.get("privileged", False)},
    }
    if backend == "ort-cuda":
        container["resources"] = {"limits": {"nvidia.com/gpu": "1"}}
    pod = {
        "apiVersion": "v1", "kind": "Pod",
        "metadata": {"generateName": "chill-runtime-inspect-",
                     "labels": {"app.kubernetes.io/name": "chill-runtime-inspect"}},
        "spec": {"nodeName": node, "restartPolicy": "Never", "tolerations": [{"operator": "Exists"}],
                 "securityContext": {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532,
                                     "supplementalGroups": access.get("supplementalGroups", [])},
                 "containers": [container]},
    }
    name = kubectl("create", "-f", "-", "-o", "name", stdin=json.dumps(pod)).strip()
    try:
        deadline = time.monotonic() + int(os.environ.get("INSPECT_TIMEOUT_SECONDS", "3600"))
        while True:
            status = json.loads(kubectl("get", name, "-o", "json"))["status"]
            state = (status.get("containerStatuses") or [{}])[0].get("state", {})
            waiting = state.get("waiting", {}).get("reason")
            if status.get("phase") in ("Succeeded", "Failed") or waiting in ("ErrImagePull", "ImagePullBackOff"):
                break
            if time.monotonic() > deadline:
                fail(f"{image} on {node}: probe did not finish; last status {json.dumps(status)}")
            time.sleep(5)
        if status.get("phase") != "Succeeded":
            fail(f"{image} on {node}: probe failed: {json.dumps(state or status)}")
        print(f"inspected {image} on {node}", file=sys.stderr)
        return json.loads(state["terminated"]["message"])
    finally:
        kubectl("delete", name, "--wait=false")


def entry(device_class, declaration, **blocks):
    lines = [f"    - deviceClass: {device_class}", "      runtime:",
             "        payload: '" + json.dumps(declaration, separators=(",", ":")).replace("'", "''") + "'"]
    lines += [f"      {key}: {json.dumps(value)}" for key, value in blocks.items()]
    return "\n".join(lines)


def cuda_entry(device_class, architecture, image, node, groups, probe=None):
    """One declaration bound to node and the GPU UUID the probe read there."""
    access = {"privileged": False, "supplementalGroups": groups}
    facts = (probe or inspect)(image, node, "ort-cuda", access)
    if len(facts["gpuUUIDs"]) != 1:
        fail(f"{image} on {node}: expected one allocated GPU, found {facts['gpuUUIDs']}")
    access = {**access, "resources": {"nvidia.com/gpu": "1"}, "gpuUUID": facts["gpuUUIDs"][0]}
    if not groups:
        del access["supplementalGroups"]
    return entry(device_class, payload(image, architecture, "ort-cuda", facts), node=node, access=access)


def main():
    entries, probed = [], {}
    for device_class, architecture, cuda_variable, cuda_nodes, groups in CLASSES:
        variable, node = CPU[architecture]
        image = os.environ.get(variable) or fail(f"{variable} is required")
        if image not in probed:
            probed[image] = payload(image, architecture, "ort-cpu", inspect(image, node, "ort-cpu", {}))
        entries.append(entry(device_class, probed[image]))
        if not cuda_variable:
            continue
        image = os.environ.get(cuda_variable)
        if not image:
            print(f"{device_class}: CUDA declaration omitted, {cuda_variable} is unset", file=sys.stderr)
            continue
        for cuda_node in cuda_nodes:
            entries.append(cuda_entry(device_class, architecture, image, cuda_node, groups))
    image = os.environ.get("RKNN_IMAGE")
    if image:
        facts = inspect(image, RKNN_NODE, "rknnlite", RKNN_ACCESS)
        if "rockchip,rk3588" not in facts["compatible"]:
            fail(f"{RKNN_NODE}: device tree {facts['compatible']} lacks rockchip,rk3588")
        host = {**RKNN_HOST,
                "driver": {"name": "rknpu", "versionPath": "/sys/module/rknpu/version", "version": facts["driverVersion"]},
                "platform": {"compatiblePath": "/proc/device-tree/compatible", "compatibleToken": "rockchip,rk3588"}}
        entries.append(entry(RKNN_CLASS, payload(image, "arm64", "rknnlite", facts), host=host, access=RKNN_ACCESS))
    else:
        print(f"{RKNN_CLASS}: RKNN declaration omitted, RKNN_IMAGE is unset", file=sys.stderr)
    print("profiling:\n  executionDeclarations:\n" + "\n".join(entries))


def self_check():
    image = "registry/chill/chill-firstfloor@sha256:" + "a" * 64
    facts = {"machine": "aarch64", "ortVersion": "1.24.0", "providers": ["CUDAExecutionProvider", "CPUExecutionProvider"],
             "packages": {"onnxruntime": None, "onnxruntime-gpu": "1.24.0", "numpy": "1.26.4", "rknn-toolkit-lite2": None}}
    declaration = payload(image, "arm64", "ort-cuda", facts)
    assert declaration["packages"] == [{"name": "onnxruntime-gpu", "version": "1.24.0"},
                                       {"name": "numpy", "version": "1.26.4"}]
    assert declaration["runtime"] == {"family": "firstfloor", "backends": ["ort-cuda"]}
    for broken in ({**facts, "machine": "x86_64"}, {**facts, "providers": ["CPUExecutionProvider"]}):
        try:
            payload(image, "arm64", "ort-cuda", broken)
        except SystemExit:
            continue
        raise AssertionError("drifted probe facts were accepted")
    text = entry("jetson-orin-nano-8g", declaration, access={"gpuUUID": "GPU-x"})
    assert json.loads(text.split("payload: '")[1].split("'\n")[0]) == declaration
    amd64 = {**facts, "machine": "x86_64"}
    texts = [cuda_entry("edge-server-rtx-2080-ti-64g", "amd64", image, node, [],
                        probe=lambda _i, n, _b, _a: {**amd64, "gpuUUIDs": [f"GPU-{n}"]})
             for node in ("genesis2", "genesis3")]
    for node, text in zip(("genesis2", "genesis3"), texts):
        assert f'      node: "{node}"' in text and f'"gpuUUID": "GPU-{node}"' in text, text
    print("runtime declarations self-check: ok")


if __name__ == "__main__":
    self_check() if sys.argv[1:] == ["--self-check"] else main()

"""Fixed CHILL image builds and registry-verified publication receipts.

Planning is local-only. All processes, including read-only registry requests,
use the caller's bounded evidence-writing run(argv, name, timeout=...) callback.
"""
import copy
import hashlib
import io
import json
from pathlib import Path
import re
import tarfile
from urllib.parse import urlsplit


POWER_SOURCE_SHA = "d39c9576f5b2f4ee6a48034e6b95eba28e43cd4e"
REVISION_LABEL = "org.opencontainers.image.revision"
MANIFEST_ACCEPT = ", ".join((
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
))
MULTI = ["linux/amd64", "linux/arm64"]
# These are deployment defaults from build-images.sh, not a blanket platform flag.
ROLES = {
    "server": ("chill-server", "serving-entrypoint", ["linux/amd64"], "servingEntrypoint.image"),
    "profiler": ("profiler", "profiler", ["linux/amd64"], "profiler.image"),
    "dashboard": ("dashboard", "dashboard", ["linux/amd64"], "dashboard.image"),
    "node-agent": ("node-agent", "node-agent", MULTI, "nodeAgent.image"),
    "operator": ("operator", "operator", ["linux/amd64"], "operator.image"),
    "query": ("chill-query", "query", ["linux/amd64"], "query.image"),
    "metric-observer": ("metric-observer", "metric-observer", MULTI, "profiling.metricObserverImage"),
    "workload-observer": ("workload-observer", "workload-observer", MULTI, "profiling.workloadObserverImage"),
    "load-generator": ("load-generator", "load-generator", ["linux/amd64"], "loadGenerator.image"),
    "evaluation-harness": ("evaluation-harness", "evaluation-harness", ["linux/amd64"], None),
    "firstfloor": ("firstfloor", "firstfloor", MULTI, None),
    "firstfloor-cuda": ("firstfloor", "firstfloor", ["linux/amd64"], None),
    "firstfloor-l4t-r32": ("firstfloor", "firstfloor", ["linux/arm64"], None),
    "firstfloor-l4t-r35": ("firstfloor", "firstfloor", ["linux/arm64"], None),
    "firstfloor-l4t-r36": ("firstfloor", "firstfloor", ["linux/arm64"], None),
    "firstfloor-rknn": ("firstfloor", "firstfloor", ["linux/arm64"], None),
}
RUNTIME_ARGS = {
    "RUNTIME_IMAGE", "APT_PACKAGES", "NUMPY_VERSION", "ONNXRUNTIME_VERSION",
    "ORT_PACKAGE", "PIP_EXTRA_INDEX_URL", "EXTRA_PACKAGES", "MODULE_PACKAGES",
}


def _make_defaults(source):
    """Read only literal defaults; never execute Make or evaluate its shell forms."""
    values = {}
    text = (Path(source) / "Makefile").read_text()
    for key, value in re.findall(r"^(FIRSTFLOOR_[A-Z0-9_]+)\s*\?=\s*(.*?)\s*$", text, re.M):
        if "$" not in value:
            values[key] = value
    return values


def catalog(source=None):
    result = {}
    for role, (dockerfile, repository, platforms, mapping) in ROLES.items():
        result[role] = {
            "role": role, "dockerfile": "build/docker/" + dockerfile + ".Dockerfile",
            "repository": "chill-" + repository, "platforms": list(platforms),
            "deploymentMapping": mapping, "buildArgs": {},
        }
    defaults = _make_defaults(source if source is not None else Path(__file__).resolve().parents[2])
    result["firstfloor-cuda"]["buildArgs"] = {
        "RUNTIME_IMAGE": defaults["FIRSTFLOOR_CUDA_BASE"], "APT_PACKAGES": "python3 python3-pip",
        "ORT_PACKAGE": "onnxruntime-gpu", "ONNXRUNTIME_VERSION": defaults["FIRSTFLOOR_CUDA_ORT_VERSION"],
    }
    result["firstfloor-l4t-r36"]["buildArgs"] = {
        "RUNTIME_IMAGE": defaults["FIRSTFLOOR_L4T_R36_BASE"], "APT_PACKAGES": "python3 python3-pip",
        "ORT_PACKAGE": "onnxruntime-gpu", "ONNXRUNTIME_VERSION": defaults["FIRSTFLOOR_L4T_R36_ORT_VERSION"],
        "PIP_EXTRA_INDEX_URL": defaults["FIRSTFLOOR_L4T_R36_INDEX"],
    }
    result["firstfloor-l4t-r32"]["buildArgs"] = {
        "RUNTIME_IMAGE": "l4t-r32", "NUMPY_VERSION": "1.24.4", "ORT_PACKAGE": "onnxruntime-gpu",
        "ONNXRUNTIME_VERSION": "1.11.0", "MODULE_PACKAGES": defaults["FIRSTFLOOR_L4T_R32_MODULE_PACKAGES"],
    }
    result["firstfloor-l4t-r35"]["buildArgs"] = {
        "RUNTIME_IMAGE": "l4t-r35", "NUMPY_VERSION": "1.26.4", "ORT_PACKAGE": "onnxruntime-gpu",
        "ONNXRUNTIME_VERSION": "1.18.0",
    }
    result["firstfloor-rknn"]["buildArgs"] = {"EXTRA_PACKAGES": defaults["FIRSTFLOOR_RKNN_PACKAGES"]}
    return result


def _digest(value):
    if not isinstance(value, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", value):
        raise ValueError("invalid image digest")
    return value


def _reference(value, repository):
    if not isinstance(value, str):
        raise ValueError("image reference must be a string")
    tagged = value.split("@", 1)[0]
    if not tagged.startswith(repository + ":") or not re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}", tagged[len(repository) + 1:]):
        raise ValueError("foreign repository or malformed image tag")
    if "@" in value:
        _digest(value.split("@", 1)[1])
    return tagged


def _prebuilt(path, role, sha, spec):
    receipt = json.loads(Path(path).read_text())
    if not isinstance(receipt, dict) or receipt.get("sourceSHA") != sha:
        raise ValueError("prebuilt sourceSHA mismatch: " + role)
    entry = receipt.get("roles", {}).get(role) if "roles" in receipt else receipt
    return receipt_entry(entry, role, sha, spec)


def receipt_entry(entry, role, sha, spec):
    """Validate and freeze one role's existing registry publication evidence."""
    if not isinstance(entry, dict):
        raise ValueError("missing prebuilt role: " + role)
    _reference(entry.get("image"), spec["repository"])
    _digest(entry.get("digest"))
    if "@" in entry["image"] and entry["image"].split("@", 1)[1] != entry["digest"]:
        raise ValueError("prebuilt reference/digest mismatch: " + role)
    if entry.get("dockerfile") != spec["dockerfile"] or entry.get("buildArgs") != spec["buildArgs"]:
        raise ValueError("prebuilt build provenance mismatch: " + role)
    platforms = entry.get("platforms")
    if not isinstance(platforms, list) or len(platforms) != len(set(platforms)) or set(platforms) != set(spec["platforms"]):
        raise ValueError("prebuilt platform mismatch: " + role)
    digests = entry.get("platformDigests")
    if not isinstance(digests, list) or not digests:
        raise ValueError("missing prebuilt platform digests: " + role)
    for digest in digests:
        _digest(digest)
    platform_receipts = entry.get("platformReceipts")
    if not isinstance(platform_receipts, list) or len(platform_receipts) != len(platforms):
        raise ValueError("missing prebuilt platform receipts: " + role)
    receipt_platforms, receipt_digests = set(), set()
    for platform_receipt in platform_receipts:
        if not isinstance(platform_receipt, dict) or platform_receipt.get("revision") != sha:
            raise ValueError("prebuilt platform receipt revision mismatch: " + role)
        platform = platform_receipt.get("platform")
        if not isinstance(platform, str) or platform not in platforms or platform in receipt_platforms:
            raise ValueError("prebuilt platform receipt mapping mismatch: " + role)
        receipt_platforms.add(platform)
        receipt_digests.add(_digest(platform_receipt.get("digest")))
        _digest(platform_receipt.get("configDigest"))
        if not isinstance(platform_receipt.get("variant"), str):
            raise ValueError("missing prebuilt platform variant receipt: " + role)
    if len(digests) != len(set(digests)) or receipt_digests != set(digests):
        raise ValueError("prebuilt platform digest mapping mismatch: " + role)
    if role == "evaluation-harness" and entry.get("powerSourceSHA") != POWER_SOURCE_SHA:
        raise ValueError("prebuilt harness power-source mismatch")
    return copy.deepcopy(entry)


def plan(recipe, source_sha, source, out):
    if not isinstance(source_sha, str) or not re.fullmatch(r"[0-9a-f]{40}", source_sha):
        raise ValueError("source SHA must be a resolved full commit")
    registry = recipe.get("registry", "")
    # Explicit host and namespace; never accept URL credentials or exporter options.
    if not isinstance(registry, str) or not re.fullmatch(r"[a-zA-Z0-9.-]+(?::[0-9]+)?/[a-z0-9]+(?:[._/-][a-z0-9]+)*", registry):
        raise ValueError("registry must be host[:port]/namespace")
    scheme = recipe.get("registryScheme", "https")
    builder = recipe.get("builder", "chill-builder")
    tag = recipe.get("tag", "sha-" + source_sha[:12])
    if scheme not in ("http", "https") or not isinstance(builder, str) or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", builder):
        raise ValueError("invalid registry scheme or builder")
    roles = catalog(source)
    selected = recipe.get("images", [])
    if not isinstance(selected, list) or not selected:
        raise ValueError("images must be a nonempty array")
    plans, seen = [], set()
    for item in selected:
        options = {"role": item} if isinstance(item, str) else item
        if not isinstance(options, dict) or set(options) - {"role", "platforms", "buildArgs", "prebuilt"}:
            raise ValueError("invalid image settings")
        role = options.get("role")
        if not isinstance(role, str) or role not in roles or role in seen:
            raise ValueError("unknown or duplicate image role: " + str(role))
        seen.add(role)
        spec = copy.deepcopy(roles[role])
        spec["repository"] = registry + "/" + spec["repository"]
        # Match Make's runtime variants: same repository, variant tag suffix.
        suffix = role[len("firstfloor"):] if role.startswith("firstfloor-") else ""
        spec["image"] = spec["repository"] + ":" + tag + suffix
        _reference(spec["image"], spec["repository"])
        platforms = options.get("platforms", spec["platforms"])
        if not isinstance(platforms, list) or not platforms or any(p not in MULTI for p in platforms) or len(platforms) != len(set(platforms)):
            raise ValueError("platforms must be unique linux/amd64 or linux/arm64 entries")
        if role.startswith("firstfloor-") and set(platforms) != set(spec["platforms"]):
            raise ValueError("vendor runtime platform is fixed: " + role)
        spec["platforms"] = platforms
        overrides = options.get("buildArgs", {})
        allowed = RUNTIME_ARGS if role.startswith("firstfloor") else ({"KUBECTL_VERSION"} if role == "evaluation-harness" else set())
        if not isinstance(overrides, dict) or set(overrides) - allowed or any(not isinstance(v, str) or any(c in v for c in "\r\n\x00") for v in overrides.values()):
            raise ValueError("invalid or credential-bearing buildArgs: " + role)
        for value in overrides.values():
            if "://" in value:
                parsed = urlsplit(value)
                if parsed.username or parsed.password or parsed.query or parsed.fragment:
                    raise ValueError("build argument URLs must not carry credentials or query parameters")
        spec["buildArgs"].update(overrides)
        if role == "evaluation-harness":
            spec["buildArgs"]["SOURCE_SHA"] = source_sha
            spec["powerSourceSHA"] = POWER_SOURCE_SHA
            spec["sourceRepository"] = str(recipe.get("sourceRepository", source))
        if not (Path(source) / spec["dockerfile"]).is_file():
            raise ValueError("missing Dockerfile: " + spec["dockerfile"])
        spec.update({"builder": builder, "registryScheme": scheme})
        spec["metadataFile"] = str(Path(out).resolve() / (role + "-build.json"))
        if "prebuilt" in options:
            spec["prebuilt"] = _prebuilt(options["prebuilt"], role, source_sha, spec)
            spec["image"] = _reference(spec["prebuilt"]["image"], spec["repository"])
        plans.append(spec)
    return plans


def verify(spec, source_sha, digest, run):
    """Verify the published tag, each actual platform manifest and its OCI config."""
    role = spec["role"]
    host, repository = spec["repository"].split("/", 1)
    base = spec["registryScheme"] + "://" + host + "/v2/" + repository + "/"

    def fetch(kind, identifier, name, expected=None):
        raw = run(["curl", "--fail", "--silent", "--show-error", "--max-time", "120", "-H", "Accept: " + MANIFEST_ACCEPT, base + kind + "/" + identifier], role + "-" + name + ".json", timeout=120)
        actual = "sha256:" + hashlib.sha256(raw).hexdigest()
        if expected is not None and actual != _digest(expected):
            raise ValueError("registry content digest mismatch: " + role + "/" + name)
        value = json.loads(raw)
        if not isinstance(value, dict) or (kind == "manifests" and value.get("schemaVersion") != 2):
            raise ValueError("malformed registry document: " + role + "/" + name)
        return value, actual

    tag = _reference(spec["image"], spec["repository"]).rsplit(":", 1)[1]
    manifest, _ = fetch("manifests", tag, "manifest", digest)
    receipts = []
    if "manifests" in manifest:
        if not isinstance(manifest["manifests"], list) or not manifest["manifests"]:
            raise ValueError("empty registry index: " + role)
        candidates = []
        for descriptor in manifest["manifests"]:
            platform = descriptor.get("platform", {})
            if platform.get("os") == "unknown" and platform.get("architecture") == "unknown" and descriptor.get("annotations", {}).get("vnd.docker.reference.type") == "attestation-manifest":
                continue
            platform_name = platform.get("os", "") + "/" + platform.get("architecture", "")
            if platform_name not in spec["platforms"]:
                raise ValueError("foreign registry platform: " + role + "/" + platform_name)
            candidates.append((descriptor["digest"], platform_name, platform.get("variant", "")))
    else:
        candidates = [(digest, None, "")]
    for index, (child_digest, advertised, variant) in enumerate(candidates):
        _digest(child_digest)
        child = manifest if child_digest == digest else fetch("manifests", child_digest, "platform-" + str(index), child_digest)[0]
        config_digest = _digest(child.get("config", {}).get("digest"))
        config, _ = fetch("blobs", config_digest, "config-" + str(index), config_digest)
        platform = config.get("os", "") + "/" + config.get("architecture", "")
        if platform not in spec["platforms"] or (advertised is not None and platform != advertised):
            raise ValueError("registry config platform mismatch: " + role)
        if variant and config.get("variant") and variant != config["variant"]:
            raise ValueError("registry config variant mismatch: " + role)
        revision = config.get("config", {}).get("Labels", {}).get(REVISION_LABEL)
        if revision != source_sha:
            raise ValueError("registry OCI revision mismatch: " + role)
        receipts.append({"platform": platform, "digest": child_digest, "configDigest": config_digest, "revision": revision, "variant": config.get("variant") or variant})
    actual = [entry["platform"] for entry in receipts]
    if len(actual) != len(set(actual)) or set(actual) != set(spec["platforms"]):
        raise ValueError("registry platform set mismatch: " + role)
    entry = {key: copy.deepcopy(spec[key]) for key in ("dockerfile", "buildArgs", "platforms")}
    entry.update({"image": _reference(spec["image"], spec["repository"]) + "@" + digest, "digest": digest,
                  "sourceSHA": source_sha,
                  "platformDigests": [r["digest"] for r in receipts], "platformReceipts": receipts,
                  "deploymentMapping": spec["deploymentMapping"]})
    if role == "evaluation-harness":
        entry["powerSourceSHA"] = POWER_SOURCE_SHA
    previous = spec.get("prebuilt") if "prebuilt" in spec else spec.get("reuse", {}).get("receipt")
    if previous is not None and set(previous["platformDigests"]) != set(entry["platformDigests"]):
        raise ValueError("previous registry platform digest mismatch: " + role)
    return entry


def _power_context(spec, out, run):
    context = Path(out).resolve() / "power-source"
    raw = run(["git", "-c", "core.attributesFile=/dev/null", "-C", spec["sourceRepository"], "archive", "--format=tar", POWER_SOURCE_SHA], "power-source.tar", timeout=120)
    context.mkdir()
    with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
        # Only the power build's Go inputs are needed, not unrelated repository assets.
        members = [m for m in archive.getmembers() if m.name in ("go.mod", "go.sum") or m.name.split("/", 1)[0] in ("api", "internal", "hack")]
        for member in members:
            path = Path(member.name)
            if path.is_absolute() or ".." in path.parts or not (member.isfile() or member.isdir()):
                raise ValueError("unsafe pinned power-source archive")
        archive.extractall(str(context), members=members)
    return context


def build(plans, source_sha, source, out, run):
    """Publish selected builds or reverify existing receipts, never prune caches."""
    source = Path(source).resolve()
    publication = {"sourceSHA": source_sha, "roles": {}, "runtimeBuilds": 0}
    for spec in plans:
        role = spec["role"]
        # The publication's execution source does not relabel reused OCI images.
        revision = source_sha
        if "prebuilt" in spec:
            digest = spec["prebuilt"]["digest"]
        elif "reuse" in spec:
            revision = spec["reuse"]["sourceSHA"]
            digest = spec["reuse"]["receipt"]["digest"]
        else:
            argv = ["docker", "buildx", "build", "--builder", spec["builder"], "--platform", ",".join(spec["platforms"]),
                    "--label", REVISION_LABEL + "=" + source_sha, "--metadata-file", spec["metadataFile"],
                    "-f", str(Path(source) / spec["dockerfile"])]
            for key, value in sorted(spec["buildArgs"].items()):
                argv.extend(["--build-arg", key + "=" + value])
            if role == "evaluation-harness":
                argv.extend(["--build-context", "power-source=" + str(_power_context(spec, out, run))])
            exporter = "type=image,name=" + spec["image"] + ",push=true"
            if spec["registryScheme"] == "http":
                exporter += ",registry.insecure=true"
            argv.extend(["--output", exporter, str(source)])
            run(argv, role + "-build.log", timeout=1800)
            digest = _digest(json.loads(Path(spec["metadataFile"]).read_text())["containerimage.digest"])
            if role.startswith("firstfloor"):
                publication["runtimeBuilds"] += 1
        entry = verify(spec, revision, digest, run)
        entry["action"] = "prebuilt" if "prebuilt" in spec else ("reuse" if "reuse" in spec else "build")
        for key in ("reason", "inputPaths"):
            if key in spec:
                entry[key] = copy.deepcopy(spec[key])
        publication["roles"][role] = entry
        # Keep successful receipts even when a subsequent image fails.
        (Path(out) / "publication.json").write_text(json.dumps(publication, indent=2) + "\n")
    (Path(out) / "overlay.json").write_text(json.dumps(overlay(publication), indent=2) + "\n")
    return publication


def overlay(publication):
    values = {}
    for role, entry in publication["roles"].items():
        mapping = ROLES[role][3]
        if mapping is None:
            continue
        section, key = mapping.split(".")
        if key == "image":
            repository, tag = entry["image"].split("@", 1)[0].rsplit(":", 1)
            values.setdefault(section, {})[key] = {"repository": repository, "tag": tag + "@" + entry["digest"]}
        else:
            values.setdefault(section, {})[key] = entry["image"]
    return values

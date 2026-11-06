"""Offline reuse decisions from an existing publication and Git history only."""
import copy
import json
from pathlib import Path, PurePosixPath
import re
import shlex
import subprocess

import images


def _inputs(spec, source):
    dockerfile = spec["dockerfile"]
    paths = {dockerfile, ".dockerignore", dockerfile + ".dockerignore"}
    try:
        text = (Path(source) / dockerfile).read_text()
    except OSError:
        return sorted(paths), "Dockerfile inputs unavailable"
    # ponytail: ignore exclusions conservatively; COPY . watches the whole tree.
    # This can rebuild excluded files, but never misses a newly included file.
    logical, pending = [], ""
    for line in text.splitlines():
        if line.lstrip().startswith("#"):
            if re.match(r"\s*#\s*escape\s*=", line, re.I):
                return sorted(paths), "unsupported Dockerfile escape directive"
            continue
        line = line.rstrip()
        if line.endswith("\\"):
            pending += line[:-1] + " "
        else:
            logical.append(pending + line)
            pending = ""
    if pending:
        return sorted(paths), "unfinished Dockerfile continuation"
    known = {"FROM", "ARG", "ENV", "LABEL", "WORKDIR", "USER", "EXPOSE", "VOLUME",
             "ENTRYPOINT", "CMD", "HEALTHCHECK", "STOPSIGNAL", "SHELL", "MAINTAINER"}
    for line in logical:
        parts = line.strip().split(None, 1)
        instruction = parts[0].upper() if parts else ""
        body = parts[1] if len(parts) > 1 else ""
        if not instruction:
            continue
        if instruction == "RUN":
            if "<<" in body:
                return sorted(paths), "unsupported Dockerfile heredoc"
            for mount in re.findall(r"--mount=([^\s]+)", body):
                fields = dict(part.split("=", 1) if "=" in part else (part, "") for part in mount.split(","))
                if fields.get("type", "bind") == "bind" and (not fields.get("from") or "$" in fields["from"]):
                    return sorted(paths), "RUN mount has local or dynamic context inputs"
            continue
        if instruction in known:
            continue
        if instruction not in {"COPY", "ADD"}:
            return sorted(paths), "unsupported Dockerfile instruction: " + instruction
        flags = {}
        body = body.lstrip()
        while body.startswith("--"):
            flag, sep, body = body.partition(" ")
            key, equals, value = flag[2:].partition("=")
            if not sep or key not in {"from", "chown", "chmod", "link", "parents", "checksum", "keep-git-dir"}:
                return sorted(paths), "unsupported COPY/ADD flag: " + flag
            if key in {"from", "chown", "chmod", "checksum"} and (not equals or not value or "$" in value):
                return sorted(paths), "dynamic or unknown COPY/ADD flag: " + flag
            flags[key] = value
            body = body.lstrip()
        if "<<" in body:
            return sorted(paths), "unsupported COPY/ADD heredoc"
        try:
            operands = json.loads(body) if body.startswith("[") else shlex.split(body)
        except (ValueError, TypeError):
            return sorted(paths), "unrecognized COPY/ADD operands"
        if not isinstance(operands, list) or len(operands) < 2 or any(not isinstance(p, str) for p in operands):
            return sorted(paths), "unrecognized COPY/ADD operands"
        if "from" in flags:
            continue  # Other stages and named/external contexts are not local paths.
        for operand in operands[:-1]:
            if instruction == "ADD" and operand.startswith("https://") and re.fullmatch(r"sha256:[0-9a-f]{64}", flags.get("checksum", "")):
                continue
            if not operand or any(c in operand for c in "$*?[]\\") or ":" in operand or operand.startswith("/"):
                return sorted(paths), "dynamic, wildcard or remote COPY/ADD input: " + operand
            path = PurePosixPath(operand)
            if ".." in path.parts:
                return sorted(paths), "COPY/ADD input escapes context: " + operand
            normalized = str(path)
            paths.add(normalized)
            local = Path(source)
            for part in path.parts:
                local /= part
                if local.is_symlink():
                    return sorted(paths), "symlink COPY/ADD input: " + normalized
    return sorted(paths), None


def _git(repository, *args):
    return subprocess.run(["git", "-C", str(repository), *args], stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, check=False)


def _revision_reason(repository, revision):
    resolved = _git(repository, "rev-parse", "--verify", revision + "^{commit}")
    if resolved.returncode or resolved.stdout.decode().strip() != revision:
        return "source commit unavailable in repository: " + revision
    tree = _git(repository, "ls-tree", "-r", "--name-only", "-z", revision)
    if tree.returncode:
        return "Git archive attribute inspection unavailable: " + revision
    if any(path.rsplit(b"/", 1)[-1] == b".gitattributes" for path in tree.stdout.split(b"\x00")):
        return "tracked .gitattributes may transform Git archive inputs: " + revision
    return None


def _unusable(spec, reason, pending):
    if pending:
        raise ValueError("pending publication cannot be reused: " + spec["role"] + ": " + reason)
    spec["reason"] = reason


def apply(plans, source_sha, repository, source, publication_path, pending_builds=None):
    """Freeze old receipts or explicitly selected, still-unverified build output."""
    publication = None
    if publication_path is not None:
        path = Path(publication_path)
        if not path.is_absolute():
            raise ValueError("reusePublication must be an absolute publication.json path")
        publication = json.loads(path.read_text())
        if not isinstance(publication, dict) or not isinstance(publication.get("roles"), dict):
            raise ValueError("invalid reuse publication roles")
    pending_builds = {} if pending_builds is None else pending_builds
    if (not isinstance(pending_builds, dict)
            or any(not isinstance(role, str) or not isinstance(path, str) or not path for role, path in pending_builds.items())):
        raise ValueError("reusePendingBuilds must map roles to old build metadata")
    pending_plans = {}
    if pending_builds:
        if publication is None:
            raise ValueError("reusePendingBuilds requires reusePublication")
        selected = {spec["role"]: spec for spec in plans}
        if any(role not in selected or role in publication["roles"] or "prebuilt" in selected[role]
               for role in pending_builds):
            raise ValueError("pending role must be selected and have no existing receipt")
        old_sha = publication.get("sourceSHA")
        if not isinstance(old_sha, str) or not re.fullmatch(r"[0-9a-f]{40}", old_sha):
            raise ValueError("invalid pending publication sourceSHA")
        plan_path = Path(publication_path).parent / "image-plan.json"
        old_plans = json.loads(plan_path.read_text())
        if (not isinstance(old_plans, list) or any(not isinstance(p, dict) or not isinstance(p.get("role"), str) for p in old_plans)
                or len({p["role"] for p in old_plans}) != len(old_plans)):
            raise ValueError("invalid pending publication image plan")
        pending_plans = {p["role"]: p for p in old_plans if p["role"] in pending_builds}
        if set(pending_plans) != set(pending_builds):
            raise ValueError("missing pending role in frozen image plan")
    result = copy.deepcopy(plans)
    revision_reasons = {}
    attributes_reason = None
    if publication is not None:
        attributes = _git(repository, "rev-parse", "--git-path", "info/attributes")
        if attributes.returncode:
            attributes_reason = "repository archive attribute inspection unavailable"
        else:
            path = Path(attributes.stdout.decode().strip())
            if not path.is_absolute():
                path = Path(repository) / path
            if path.exists():
                attributes_reason = "repository info/attributes may transform Git archive inputs"
    for spec in result:
        spec.pop("reuse", None)
        spec.pop("pendingPublication", None)
        paths, unknown = _inputs(spec, source)
        spec.update(action="build", reason="no reuse publication configured", inputPaths=paths)
        if "prebuilt" in spec:
            spec.update(action="prebuilt", reason="explicit prebuilt receipt")
            continue
        if publication is None:
            continue
        role = spec["role"]
        pending = role in pending_builds
        entry = pending_plans.get(role) if pending else publication["roles"].get(role)
        if entry is None:
            spec["reason"] = "no previous receipt for role: " + role
            continue
        if not isinstance(entry, dict):
            raise ValueError("invalid reuse role receipt: " + role)
        if pending:
            origin = entry.get("prebuilt", entry.get("reuse", entry.get("pendingPublication", entry)))
            old_sha = origin.get("sourceSHA", publication["sourceSHA"] if origin is entry else None)
            if not isinstance(old_sha, str) or not re.fullmatch(r"[0-9a-f]{40}", old_sha):
                _unusable(spec, "previous build origin unavailable", pending)
            metadata_path = Path(pending_builds[role])
            for key in ("repository", "registryScheme"):
                if entry.get(key) != spec.get(key):
                    raise ValueError("pending image plan provenance mismatch: " + role + "/" + key)
            frozen = images.pending_build(entry, metadata_path)
            frozen.update(sourceSHA=old_sha, publicationFile=str(publication_path), planFile=str(plan_path))
        else:
            old_sha = entry.get("sourceSHA")
            if not isinstance(old_sha, str) or not re.fullmatch(r"[0-9a-f]{40}", old_sha):
                spec["reason"] = "previous build origin unavailable"
                continue
        expected = copy.deepcopy(spec)
        if "SOURCE_SHA" in expected["buildArgs"]:
            expected["buildArgs"]["SOURCE_SHA"] = old_sha
        mismatch = next((reason for key, reason in (
            ("dockerfile", "Dockerfile changed"), ("buildArgs", "effective build arguments changed"),
            ("powerSourceSHA", "pinned power source changed")) if entry.get(key) != expected.get(key)), None)
        if mismatch:
            _unusable(spec, mismatch, pending)
            continue
        platforms = entry.get("platforms")
        if (not isinstance(platforms, list) or any(not isinstance(p, str) for p in platforms)
                or len(platforms) != len(set(platforms)) or sorted(platforms) != sorted(expected["platforms"])):
            _unusable(spec, "platforms changed", pending)
            continue
        if not pending:
            frozen = images.receipt_entry(entry, role, expected)
        if unknown:
            _unusable(spec, unknown, pending)
            continue
        if attributes_reason:
            _unusable(spec, attributes_reason, pending)
            continue
        blocked = False
        for revision in (old_sha, source_sha):
            if revision not in revision_reasons:
                revision_reasons[revision] = _revision_reason(repository, revision)
            if revision_reasons[revision]:
                _unusable(spec, revision_reasons[revision], pending)
                blocked = True
                break
        if blocked:
            continue
        diff = _git(repository, "--literal-pathspecs", "diff", "--quiet", "--no-ext-diff", "--no-textconv", "--no-renames",
                    old_sha, source_sha, "--", *paths)
        if diff.returncode:
            _unusable(spec, "Git inputs changed" if diff.returncode == 1 else "Git input comparison unavailable", pending)
            continue
        spec.update(reason="Git build inputs unchanged from " + old_sha,
                    image=entry["image"].split("@", 1)[0], buildArgs=copy.deepcopy(expected["buildArgs"]))
        if pending:
            spec.update(action="verify-pending", pendingPublication=frozen)
        else:
            spec.update(action="reuse", reuse={"sourceSHA": old_sha, "receipt": frozen})
    return result

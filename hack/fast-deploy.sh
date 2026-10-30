#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

# Upgrade is deliberately isolated from the reset/uninstall development path.
if [[ "${1:-}" == upgrade ]]; then
	shift
	exec python3 - "$@" <<'PY'
import argparse, copy, datetime, difflib, fcntl, hashlib, json, os, pathlib, re, shlex, subprocess, sys, tempfile, time, urllib.parse

p = argparse.ArgumentParser(description="Upgrade control images only; never reset the release or build runtime images.")
p.add_argument("--source", required=True)
p.add_argument("--control", required=True)
p.add_argument("--images", default="auto")
p.add_argument("--dry-run", action="store_true")
p.add_argument("--chart-diff-approved", type=pathlib.Path, help="File containing the exact approved canonical live-to-source chart diff")
p.add_argument("--values", type=pathlib.Path, help="Explicit chart values overlay, covered by exact chart-diff approval")
p.add_argument("--self-check", action="store_true", help="Run local selection/deadline regression checks only")
p.add_argument("--token-safety-margin-seconds", type=float, default=120)
a = p.parse_args()
if not re.fullmatch(r"[0-9]+", a.control):
    p.error("--control must contain digits")
roles = {"server": ("servingEntrypoint", "serving-entrypoint"), "profiler": ("profiler", "profiler"), "dashboard": ("dashboard", "dashboard"), "node-agent": ("nodeAgent", "node-agent")}
dockerfiles = {"server": "chill-server", "profiler": "profiler", "dashboard": "dashboard", "node-agent": "node-agent"}
selected = a.images.split(",")
if a.images != "auto" and (len(set(selected)) != len(selected) or any(x not in roles for x in selected)):
    p.error("--images accepts auto or a comma-separated subset of server,profiler,dashboard,node-agent; runtime images are forbidden")
root = pathlib.Path.cwd()
sys.path.insert(0, str(root / "hack/e5"))
import auth
if not auth.math.isfinite(a.token_safety_margin_seconds) or a.token_safety_margin_seconds < 0:
    p.error("--token-safety-margin-seconds must be finite and nonnegative")
sha = subprocess.check_output(["git", "rev-parse", "--verify", a.source + "^{commit}"], text=True).strip()
base = pathlib.Path("/home/genesis1/.cache/chill-assets/ops/pools-deploy")
out = base / "deployB" / ("control-" + a.control + "-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d"))
helm = ["/usr/local/bin/helm", "--kubeconfig", "/home/genesis1/.kube/config", "--kube-context", "kubernetes-admin@kubernetes", "-n", "chill-system"]
kube = ["/usr/bin/kubectl", "--kubeconfig", "/home/genesis1/.kube/config", "--context", "kubernetes-admin@kubernetes", "-n", "chill-system"]
def build_inputs(src, role):
    dockerfile = "build/docker/" + dockerfiles[role] + ".Dockerfile"
    if role == "dashboard":
        return {"dockerfile": dockerfile, "targets": [], "directories": ["chill-dashboard"], "files": [dockerfile]}
    targets = re.findall(r"go build[^\n]* (\./cmd/[\w-]+)", (src / dockerfile).read_text())
    if not targets:
        raise RuntimeError("no Go build targets found in " + dockerfile)
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOWORK="off", GOFLAGS="", GOENV="off")
    dirs = subprocess.check_output(["go", "list", "-mod=readonly", "-deps", "-f", "{{.Dir}}", *targets], cwd=src, env=env, text=True, timeout=120).splitlines()
    local = sorted({str(pathlib.Path(d).relative_to(src)) for d in dirs if d.startswith(str(src) + os.sep)})
    return {"dockerfile": dockerfile, "targets": targets, "directories": local, "files": [dockerfile, "go.mod", "go.sum"]}
def affected(paths, inputs):
    return [x for x in paths if x in inputs["files"] or (
        (any(x.startswith(d + "/") for d in inputs["directories"]) if not inputs["targets"]
         else str(pathlib.PurePosixPath(x).parent) in inputs["directories"] and not x.endswith("_test.go")))]
def readiness_result(checks, application, remaining):
    return remaining > 0 and all(checks.values()) and application.get("status") == 200 and application.get("exitCode") == 0
def canonical_objects(raw):
    objects = [x for x in yaml.safe_load_all(raw) if isinstance(x, dict)]
    return sorted(objects, key=lambda x: (x["apiVersion"], x["kind"], x["metadata"].get("namespace", "chill-system"), x["metadata"]["name"]))
def chart_difference(deployed, rendered):
    return "".join(difflib.unified_diff(
        (json.dumps(deployed, sort_keys=True, indent=2) + "\n").splitlines(True),
        (json.dumps(rendered, sort_keys=True, indent=2) + "\n").splitlines(True),
        fromfile="live", tofile="source-chart"))
def approve_chart(difference, approval):
    if difference and (approval is None or approval.read_text() != difference):
        raise RuntimeError("source chart/live manifest drift; --chart-diff-approved must match chart-diff.txt exactly")
if a.self_check:
    server = {"files": ["go.mod", "go.sum", "build/docker/chill-server.Dockerfile"], "targets": ["./cmd/chill-server"], "directories": ["cmd/chill-server", "internal/serving"]}
    profiler = {"files": ["go.mod", "go.sum", "build/docker/profiler.Dockerfile"], "targets": ["./cmd/profiler", "./cmd/transition-observer"], "directories": ["cmd/profiler", "cmd/transition-observer", "internal/profiling"]}
    paths = ["internal/serving/reset.go", "internal/serving/reset_test.go", "cmd/console/main.go"]
    assert affected(paths, server) == [paths[0]] and affected(paths, profiler) == []
    assert affected(["cmd/transition-observer/main.go"], profiler)
    assert not affected(["internal/serving/unrelated/other.go"], server)
    assert affected(["go.mod"], server) and affected(["go.mod"], profiler)
    assert affected(["chill-dashboard/app/page.tsx"], {"files": [], "targets": [], "directories": ["chill-dashboard"]})
    import yaml
    assert chart_difference([], []) == ""
    difference = chart_difference([], [{"kind": "ClusterRole"}])
    try:
        approve_chart(difference, None)
    except RuntimeError:
        pass
    else:
        raise AssertionError("unapproved chart changes accepted")
    with tempfile.TemporaryDirectory() as temp:
        approval = pathlib.Path(temp) / "approved.txt"
        approval.write_text(difference)
        approve_chart(difference, approval)
    assert not readiness_result({"server": True}, {"status": 0, "exitCode": 7}, 10)
    assert not readiness_result({"server": True}, {"status": 503, "exitCode": 0}, 10)
    assert not readiness_result({"server": True}, {"status": 200, "exitCode": 28}, 10)
    assert not readiness_result({"server": True}, {"status": 200, "exitCode": 0}, 0)
    assert not readiness_result({"profiler": False}, {"status": 200, "exitCode": 0}, 10)
    assert readiness_result({"server": True}, {"status": 200, "exitCode": 0}, 10)
    auth.self_check()
    print("selection/deadline regression checks passed")
    sys.exit(0)
if a.dry_run:
    print("LOCAL PLAN ONLY: no cluster, registry, image build, lock or evidence mutation.")
    print("source:", sha, "\nevidence:", out)
    with tempfile.TemporaryDirectory(prefix="chill-upgrade-plan-") as temp:
        src = pathlib.Path(temp) / "source"
        src.mkdir()
        archive = pathlib.Path(temp) / "source.tar"
        subprocess.run(["git", "archive", "--format=tar", "--output=" + str(archive), sha], check=True, timeout=120)
        subprocess.run(["tar", "-xf", str(archive), "-C", str(src)], check=True, timeout=120)
        print("build inputs:", json.dumps({r: build_inputs(src, r) for r in roles}, indent=2))
    print("git archive", sha, "→ clean source archive")
    print("helm get values chill -o json → live-values.json; auto: compare live source diff with these per-image dependency directories; source-diff.json includes matching paths and inputs")
    print("allowed builds:", a.images, "; runtime builds: FORBIDDEN (7 runtime images)")
    print("flock /tmp/chill-heavy.lock; buildx chill-builder linux/amd64 → HTTP registry.insecure=true publication and HTTP digest verification")
    print("helm template old/new → canonical chart-diff (exact --chart-diff-approved required for chart changes) + full render-diff + image-only overlay gate → kubectl apply --dry-run=server")
    print("helm upgrade --reset-values -f live-values.json -f overlay.json --wait --timeout 8m")
    print("Pod Ready + restartCount=0 + registry index/platform digest gate AND authenticated GET /api/v1/scheduler/state HTTP 200 (480s total, all queries bounded by remaining deadline); readiness.json; deploy-log.md")
    print("API: CHILL_EXPERIMENT_CONFIG d2.apiBase/caFile/tokenFile, verified TLS + Bearer, no redirects; profiler/dashboard: Pod Ready")
    sys.exit(0)
import yaml
lock = open("/tmp/chill-heavy.lock", "a")
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
os.umask(0o077)
out.mkdir(parents=True, exist_ok=False)
log = open(out / "commands.log", "a", buffering=1)
def run(cmd, name=None, limit=120):
    log.write("+ " + shlex.join(map(str, cmd)) + "\n")
    try:
        result = subprocess.run(list(map(str, cmd)), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=limit)
    except subprocess.TimeoutExpired as error:
        evidence_name = name or "command-timeout.log"
        (out / evidence_name).write_bytes(error.stdout or b"")
        (out / (evidence_name + ".stderr")).write_bytes(error.stderr or b"")
        log.write("timeout=" + str(limit) + "; evidence=" + evidence_name + "\n")
        raise
    if name:
        (out / name).write_bytes(result.stdout)
        (out / (name + ".stderr")).write_bytes(result.stderr)
    log.write("exit=" + str(result.returncode) + "\n")
    if result.returncode:
        raise RuntimeError("command failed; see " + str(out / (name or "commands.log")))
    return result.stdout
def save(name, value):
    (out / name).write_text(json.dumps(value, indent=2) + "\n")
def git(*args):
    return run(["git", *args]).decode().strip()
def image(v, role):
    value = v[roles[role][0]]["image"]
    return value["repository"] + ":" + str(value["tag"])
def source(ref):
    # Historical pools tags end in their source SHA; unknown provenance must stop.
    match = re.search(r"(?:^|-)([0-9a-f]{7,40})$", ref.split(":")[-1])
    if not match:
        raise RuntimeError("image source commit cannot be resolved: " + ref)
    return git("rev-parse", "--verify", match[1] + "^{commit}")
try:
    # Reuse CampaignOps' actual public API origin, CA and caller token contract.
    environment = json.loads(pathlib.Path(os.environ["CHILL_EXPERIMENT_CONFIG"]).read_text())
    api_config = environment["d2"]
    origin = api_config["apiBase"].rstrip("/")
    parsed = urllib.parse.urlsplit(origin)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.path or parsed.query or parsed.fragment:
        raise RuntimeError("d2.apiBase must be an HTTPS origin without credentials")
    ca_file = pathlib.Path(api_config["caFile"])
    token_file = pathlib.Path(api_config["tokenFile"])
    if not ca_file.is_file() or not token_file.is_file():
        raise RuntimeError("CampaignOps CA/token file is missing")
    api_url = origin + "/api/v1/scheduler/state"
    # All possibly selected builds + Helm wait + post-upgrade gate + configured slack.
    token_seconds = (len(roles) if a.images == "auto" else len(selected)) * 1800 + 510 + 480 + a.token_safety_margin_seconds
    save("token-preflight.json", auth.check(token_file, token_seconds, endpoint=origin, ca_file=ca_file))
    live = json.loads(run(helm + ["get", "values", "chill", "-o", "json"], "live-values.json"))
    run(helm + ["get", "hooks", "chill"], "live-hooks.yaml")
    run(helm + ["get", "manifest", "chill"], "live-manifest.yaml")
    archive = out / "source.tar"
    run(["git", "archive", "--format=tar", "--output=" + str(archive), sha], "archive.log")
    src = out / "source"
    src.mkdir()
    run(["tar", "-xf", archive, "-C", src], "extract.log")
    chart = src / "charts/chill"
    flags = ["-f", str(out / "live-values.json")]
    if a.values:
        (out / "approved-values.yaml").write_bytes(a.values.read_bytes())
        flags += ["-f", str(out / "approved-values.yaml")]
    before = run(helm + ["template", "chill", chart, *flags, "--is-upgrade"], "render-before.yaml")
    live_manifest = (out / "live-manifest.yaml").read_bytes() + b"\n---\n" + (out / "live-hooks.yaml").read_bytes()
    chart_diff = chart_difference(canonical_objects(live_manifest), canonical_objects(before))
    (out / "chart-diff.txt").write_text(chart_diff)
    print(chart_diff, end="", flush=True)
    approve_chart(chart_diff, a.chart_diff_approved)
    if a.chart_diff_approved:
        (out / "chart-diff-approved.txt").write_text(a.chart_diff_approved.read_text())
    changed = {}
    for role in roles:
        old = source(image(live, role))
        paths = git("diff", "--name-only", old, sha).splitlines()
        inputs = build_inputs(src, role)
        changed[role] = {"from": old, "buildInputs": inputs, "paths": affected(paths, inputs)}
    selected = [r for r in (roles if a.images == "auto" else selected) if changed[r]["paths"]]
    save("source-diff.json", changed)
    if not selected:
        raise RuntimeError("no changed control images; no upgrade performed")
    overlay, expected = {}, {}
    registry = "155.230.35.213:5000/chill/"
    for role in selected:
        key, component = roles[role]  # Only these hard-coded control Dockerfiles can reach buildx.
        repo = registry + "chill-" + component
        tag = "pools-control-" + a.control + "-" + sha[:12]
        ref = repo + ":" + tag
        metadata = out / (role + "-build.json")
        platforms = "linux/amd64,linux/arm64" if role == "node-agent" else "linux/amd64"
        run(["/usr/bin/nice", "-n", "19", "/usr/bin/docker", "buildx", "build", "--builder", "chill-builder", "--platform", platforms, "--label", "org.opencontainers.image.revision=" + sha, "--metadata-file", metadata, "-f", src / ("build/docker/" + dockerfiles[role] + ".Dockerfile"), "--output", "type=image,name=" + ref + ",push=true,registry.insecure=true", src], role + "-build.log", 1800)
        digest = json.loads(metadata.read_text())["containerimage.digest"]
        raw = run(["/usr/bin/curl", "--fail", "--silent", "--show-error", "-H", "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json", "http://155.230.35.213:5000/v2/chill/chill-" + component + "/manifests/" + tag], role + "-manifest.json")
        if "sha256:" + hashlib.sha256(raw).hexdigest() != digest:
            raise RuntimeError("registry/build digest mismatch: " + role)
        manifest = json.loads(raw)
        candidates = [x["digest"] for x in manifest.get("manifests", []) if x.get("platform", {}).get("architecture") in ("amd64", "arm64") and x.get("platform", {}).get("os") == "linux"]
        expected[role] = {"image": ref, "digest": digest, "platformDigests": candidates or [digest]}
        overlay[key] = {"image": {"repository": repo, "tag": tag}}
    save("publication.json", {"sourceSHA": sha, "roles": expected, "runtimeBuilds": 0})
    save("overlay.json", overlay)
    after = run(helm + ["template", "chill", chart, *flags, "-f", str(out / "overlay.json"), "--is-upgrade"], "render-after.yaml")
    def objects(raw):
        return {(x["apiVersion"], x["kind"], x["metadata"].get("namespace", "chill-system"), x["metadata"]["name"]): x for x in yaml.safe_load_all(raw) if isinstance(x, dict)}
    old, new, deployed = objects(before), objects(after), objects(live_manifest)
    approved = copy.deepcopy(old)
    replacements = {image(live, r): expected[r]["image"] for r in selected}
    def replace(value):
        if isinstance(value, dict):
            return {k: replace(v) for k, v in value.items()}
        if isinstance(value, list):
            return [replace(v) for v in value]
        if isinstance(value, str):
            for previous, following in replacements.items():
                value = value.replace(previous, following)
        return value
    approved = replace(approved)
    delta = [list(k) for k in sorted(deployed.keys() | new.keys()) if deployed.get(k) != new.get(k)]
    save("render-diff.json", delta)
    (out / "render-diff.txt").write_text("".join(difflib.unified_diff(live_manifest.decode().splitlines(True), after.decode().splitlines(True), fromfile="live", tofile="upgrade")))
    if new != approved or not delta:
        raise RuntimeError("unexpected render differences or no image changes")
    run(kube + ["apply", "--dry-run=server", "-f", out / "render-after.yaml"], "server-dry-run.log")
    save("token-pre-upgrade.json", auth.check(token_file, 510 + 480 + a.token_safety_margin_seconds, endpoint=origin, ca_file=ca_file))
    run(helm + ["upgrade", "chill", chart, "--reset-values", *flags, "-f", out / "overlay.json", "--wait", "--timeout", "8m"], "upgrade.log", 510)
    deadline = time.monotonic() + 480
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError("readiness/digest/restart/application HTTP 200 gate timed out")
        pods = json.loads(run(kube + ["get", "pods", "-o", "json"], "pods.json", min(120, remaining)))
        checks = {}
        for role, exp in expected.items():
            containers = [(pod, status) for pod in pods["items"] if not pod["metadata"].get("deletionTimestamp") for status in pod.get("status", {}).get("containerStatuses", []) if status["image"] == exp["image"]]
            checks[role] = bool(containers) and all(s.get("ready") and s.get("restartCount") == 0 and s.get("imageID", "").endswith(tuple([exp["digest"], *exp["platformDigests"]])) and any(c["type"] == "Ready" and c["status"] == "True" for c in pod.get("status", {}).get("conditions", [])) for pod, s in containers)
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise RuntimeError("readiness/digest/restart/application HTTP 200 gate timed out")
        application = {"method": "GET", "url": api_url, "status": 0, "attempted": False}
        if all(checks.values()):
            token = token_file.read_text().strip()
            if not token or any(c in token for c in '\r\n"\\'):
                raise RuntimeError("invalid CampaignOps bearer token")
            limit = min(10, remaining)
            # Pass credentials on stdin, never argv or commands.log; curl enforces a total request deadline.
            cmd = ["/usr/bin/curl", "--silent", "--show-error", "--max-time", str(limit), "--cacert", str(ca_file), "--config", "-", "--output", os.devnull, "--write-out", "%{http_code}", api_url]
            log.write("+ " + shlex.join(cmd) + " [Bearer header via private stdin]\n")
            try:
                response = subprocess.run(cmd, input='header = "Authorization: Bearer ' + token + '"\n', text=True, capture_output=True, timeout=min(remaining, limit + 1))
                application.update(attempted=True, status=int(response.stdout) if response.stdout.isdigit() else 0, exitCode=response.returncode, error=response.stderr)
            except subprocess.TimeoutExpired:
                application.update(attempted=True, error="request deadline exceeded")
        remaining = deadline - time.monotonic()
        ready = readiness_result(checks, application, remaining)
        save("readiness.json", {"sourceSHA": sha, "checks": checks, "application": application, "ready": ready, "runtimeBuilds": 0})
        with open(out / "application-readiness.jsonl", "a") as stream:
            stream.write(json.dumps(application) + "\n")
        if ready:
            break
        if remaining <= 0:
            raise RuntimeError("readiness/digest/restart/application HTTP 200 gate timed out")
        time.sleep(min(5, remaining))
    run(helm + ["get", "values", "chill", "-o", "json"], "post-values.json")
    with open(base / "deploy-log.md", "a") as stream:
        stream.write("\n- control-" + a.control + " source " + sha + ": upgrade Pod Ready/restarts0/digests + authenticated scheduler state HTTP200 verified; runtime builds0; evidence `" + str(out) + "`.\n")
    print("READY:", out)
except Exception as error:
    save("failure.json", {"error": str(error), "evidence": str(out)})
    print(str(error) + "; evidence: " + str(out), file=sys.stderr)
    sys.exit(1)
PY
fi

registry="${CHILL_FAST_REGISTRY:-155.230.35.213:5000}"
image_namespace="${CHILL_FAST_IMAGE_NAMESPACE:-chill}"
image_tag="${IMAGE_TAG:-}"
auto_dirty_tag="false"
operator_image="${OPERATOR_IMG:-}"
node_agent_image="${NODE_AGENT_IMG:-}"
profiler_image="${PROFILER_IMG:-}"
console_image="${CONSOLE_IMG:-}"
metric_observer_image="${METRIC_OBSERVER_IMG:-}"
workload_observer_image="${WORKLOAD_OBSERVER_IMG:-}"
serving_entrypoint_image="${SERVING_ENTRYPOINT_IMG:-}"
dashboard_image="${DASHBOARD_IMG:-}"
platforms="${PLATFORMS:-linux/amd64,linux/arm64}"
push_mode="${CHILL_FAST_PUSH_MODE:-auto}"
pull_policy="${CHILL_FAST_PULL_POLICY:-Always}"
components="${CHILL_FAST_COMPONENTS:-all}"
observe_mode="${CHILL_FAST_OBSERVE_MODE:-full}"
direct_push_fallback="${CHILL_FAST_DIRECT_PUSH_FALLBACK:-true}"

helm_bin="${HELM:-helm}"
kubectl_bin="${KUBECTL:-kubectl}"
release="${HELM_RELEASE:-chill}"
release_namespace="${HELM_NAMESPACE:-chill-system}"
chart="${HELM_CHART:-charts/chill}"
timeout="${HELM_TIMEOUT:-2m}"
values_file="${HELM_VALUES:-}"
extra_helm_set="${HELM_SET:-}"
container_tool="${CONTAINER_TOOL:-docker}"
buildx_builder="${BUILDX_BUILDER:-}"
containerd_namespace="${CHILL_FAST_CONTAINERD_NAMESPACE:-k8s.io}"
observe_attempts="${CHILL_FAST_OBSERVE_ATTEMPTS:-30}"

yes="${CHILL_FAST_YES:-false}"
dry_run="${CHILL_FAST_DRY_RUN:-false}"
skip_build="${CHILL_FAST_SKIP_BUILD:-false}"
skip_push="${CHILL_FAST_SKIP_PUSH:-false}"
skip_deploy="${CHILL_FAST_SKIP_DEPLOY:-false}"

usage() {
	cat <<EOF
Usage: $0 [options]

Build CHILL development images, push them to the internal registry, reset the
current Helm release, and print observation output.

Options:
  --registry HOST[:PORT]       Internal registry. Default: ${registry}
  --image-namespace NAME       Registry repository prefix. Default: ${image_namespace}
  --tag TAG                    Image tag. Default: sha-<git-sha>[-dirty-<UTC timestamp>]
  --platforms LIST             Buildx platforms. Default: ${platforms}
  --components NAME            all, operator, node-agent,
                               profiler, console, metric-observer,
                               workload-observer, serving-entrypoint, or
                               dashboard. Default: ${components}
  --push-mode MODE             auto, registry-direct, docker, buildx, ctr-plain-http, or none. Default: ${push_mode}
  --observe MODE               summary or full. Default: ${observe_mode}
  --helm-release NAME          Helm release. Default: ${release}
  --helm-namespace NAME        Helm namespace. Default: ${release_namespace}
  --timeout DURATION           Helm/kubectl wait timeout. Default: ${timeout}
  --yes                        Do not prompt before uninstall/install.
  --dry-run                    Print commands without running them.
  --skip-build                 Do not build images.
  --skip-push                  Do not push images.
  --skip-deploy                Do not uninstall/install; only build/push and observe.
  -h, --help                   Show this help.

Environment:
  OPERATOR_IMG                 Full operator image ref. Overrides registry/tag defaults.
  NODE_AGENT_IMG               Full node-agent image ref. Overrides registry/tag defaults.
  PROFILER_IMG                 Full Profiler image ref. Overrides registry/tag defaults.
  CONSOLE_IMG                  Full Console API image ref. Overrides registry/tag defaults.
  METRIC_OBSERVER_IMG          Full MetricObserver image ref. Overrides registry/tag defaults.
  WORKLOAD_OBSERVER_IMG        Full WorkloadObserver image ref. Overrides registry/tag defaults.
  SERVING_ENTRYPOINT_IMG       Full inference entry point image ref. Overrides registry/tag defaults.
  DASHBOARD_IMG                Full Dashboard image ref. Overrides registry/tag defaults.
  IMAGE_TAG                    Image tag when --tag is not provided.
  CONTAINER_TOOL               docker-compatible build tool. Default: docker
  BUILDX_BUILDER               Optional buildx builder for multi-platform builds.
  HELM_VALUES                  Optional Helm values file.
  HELM_SET                     Extra Helm --set flags.
  CHILL_FAST_COMPONENTS        Components to build. Default: all
  CHILL_FAST_OBSERVE_MODE      Observation detail: summary or full. Default: full
  CHILL_FAST_DIRECT_PUSH_FALLBACK
                               Fall back to ctr-plain-http if direct registry push fails. Default: true
  CHILL_FAST_CTR               ctr command for ctr-plain-http mode. Default: sudo -n ctr
  CHILL_FAST_OBSERVE_ATTEMPTS  2-second polling attempts for operator-created resources. Default: 30
EOF
}

is_true() {
	case "${1}" in
	1|true|TRUE|yes|YES|y|Y) return 0 ;;
	*) return 1 ;;
	esac
}

quote_cmd() {
	printf '%q ' "$@"
}

run() {
	printf '+ '
	quote_cmd "$@"
	printf '\n'
	if ! is_true "${dry_run}"; then
		"$@"
	fi
}

try() {
	printf '+ '
	quote_cmd "$@"
	printf '|| true\n'
	if ! is_true "${dry_run}"; then
		"$@" || true
	fi
}

require_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

split_image_ref() {
	local image="$1"
	local component="$2"
	if [[ "${image}" != *:* || "${image}" == *@* ]]; then
		echo "${component} image must be repository:tag, got ${image}" >&2
		exit 1
	fi
}

choose_push_mode() {
	if [[ "${push_mode}" != "auto" ]]; then
		printf '%s\n' "${push_mode}"
		return
	fi
	case "${registry}" in
	155.230.35.213:5000|localhost:5000|127.0.0.1:5000)
		printf '%s\n' "registry-direct"
		;;
	*)
		printf '%s\n' "docker"
		;;
	esac
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--registry)
		registry="$2"
		shift 2
		;;
	--image-namespace)
		image_namespace="$2"
		shift 2
		;;
	--tag)
		image_tag="$2"
		shift 2
		;;
	--platforms)
		platforms="$2"
		shift 2
		;;
	--components)
		components="$2"
		shift 2
		;;
	--push-mode)
		push_mode="$2"
		shift 2
		;;
	--observe)
		observe_mode="$2"
		shift 2
		;;
	--helm-release)
		release="$2"
		shift 2
		;;
	--helm-namespace)
		release_namespace="$2"
		shift 2
		;;
	--timeout)
		timeout="$2"
		shift 2
		;;
	--yes)
		yes="true"
		shift
		;;
	--dry-run)
		dry_run="true"
		shift
		;;
	--skip-build)
		skip_build="true"
		shift
		;;
	--skip-push)
		skip_push="true"
		shift
		;;
	--skip-deploy)
		skip_deploy="true"
		shift
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		echo "unknown option: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

case "${components}" in
all|operator|node-agent|profiler|console|metric-observer|workload-observer|serving-entrypoint|dashboard) ;;
*)
	echo "unsupported --components value: ${components}" >&2
	exit 1
	;;
esac

case "${observe_mode}" in
summary|full) ;;
*)
	echo "unsupported --observe mode: ${observe_mode}" >&2
	exit 1
	;;
esac

if [[ "${registry}" == http://* || "${registry}" == https://* ]]; then
	echo "--registry must be HOST[:PORT], not a URL: ${registry}" >&2
	exit 1
fi

if [[ -z "${image_tag}" ]]; then
	git_sha="$(git rev-parse --short HEAD 2>/dev/null || true)"
	if [[ -z "${git_sha}" ]]; then
		echo "IMAGE_TAG or --tag is required outside a git checkout" >&2
		exit 1
	fi
	if [[ -n "$(git status --porcelain)" ]]; then
		image_tag="sha-${git_sha}-dirty-$(date -u +%Y%m%d%H%M%S)"
		auto_dirty_tag="true"
	else
		image_tag="sha-${git_sha}"
	fi
fi

operator_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=operator"
node_agent_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=node-agent"
node_agent_actuation_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=node-agent-actuation"
profiler_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=profiler"
console_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=operator-console"
serving_entrypoint_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=serving-entrypoint"
dashboard_selector="app.kubernetes.io/instance=${release},app.kubernetes.io/component=dashboard"

component_selected() {
	local component="$1"
	if [[ "${components}" == "all" ]]; then
		case "${component}" in
		operator|node-agent|profiler|console) return 0 ;;
		*) return 1 ;;
		esac
	fi
	[[ "${components}" == "${component}" ]]
}

deployed_image() {
	local component="$1"
	local image
	case "${component}" in
	operator)
		image="$("${kubectl_bin}" -n "${release_namespace}" get deployment \
			-l "${operator_selector}" \
			-o jsonpath='{.items[0].spec.template.spec.containers[0].image}' 2>/dev/null || true)"
		;;
	node-agent)
		image="$("${kubectl_bin}" -n "${release_namespace}" get daemonset \
			-l "${node_agent_selector}" \
			-o jsonpath='{.items[0].spec.template.spec.containers[0].image}' 2>/dev/null || true)"
		;;
	profiler)
		image="$("${kubectl_bin}" -n "${release_namespace}" get deployment \
			-l "${profiler_selector}" \
			-o jsonpath='{.items[0].spec.template.spec.containers[0].image}' 2>/dev/null || true)"
		;;
	console)
		image="$("${kubectl_bin}" -n "${release_namespace}" get deployment \
			-l "${console_selector}" \
			-o jsonpath='{.items[0].spec.template.spec.containers[0].image}' 2>/dev/null || true)"
		;;
	esac
	if [[ -z "${image}" ]]; then
		echo "cannot resolve currently deployed ${component} image; set its image environment variable explicitly" >&2
		return 1
	fi
	printf '%s\n' "${image}"
}

require_cmd git
require_cmd "${container_tool}"
require_cmd "${helm_bin}"
require_cmd "${kubectl_bin}"

if [[ -z "${operator_image}" ]]; then
	if component_selected operator; then
		operator_image="${registry}/${image_namespace}/chill-operator:${image_tag}"
	else
		operator_image="$(deployed_image operator)"
	fi
fi
if [[ -z "${node_agent_image}" ]]; then
	if component_selected node-agent; then
		node_agent_image="${registry}/${image_namespace}/chill-node-agent:${image_tag}"
	else
		node_agent_image="$(deployed_image node-agent)"
	fi
fi
if [[ -z "${profiler_image}" ]] && component_selected profiler; then
	profiler_image="${registry}/${image_namespace}/chill-profiler:${image_tag}"
fi
if [[ -z "${console_image}" ]] && component_selected console; then
	console_image="${registry}/${image_namespace}/chill-console:${image_tag}"
fi
if [[ -z "${metric_observer_image}" ]] && component_selected metric-observer; then
	metric_observer_image="${registry}/${image_namespace}/chill-metric-observer:${image_tag}"
fi
if [[ -z "${workload_observer_image}" ]] && component_selected workload-observer; then
	workload_observer_image="${registry}/${image_namespace}/chill-workload-observer:${image_tag}"
fi
if [[ -z "${serving_entrypoint_image}" ]] && component_selected serving-entrypoint; then
	serving_entrypoint_image="${registry}/${image_namespace}/chill-serving-entrypoint:${image_tag}"
fi
if [[ -z "${dashboard_image}" ]] && component_selected dashboard; then
	dashboard_image="${registry}/${image_namespace}/chill-dashboard:${image_tag}"
fi
push_mode="$(choose_push_mode)"

split_image_ref "${operator_image}" "operator"
split_image_ref "${node_agent_image}" "node-agent"
if [[ -n "${profiler_image}" ]]; then
	split_image_ref "${profiler_image}" "profiler"
fi
if [[ -n "${console_image}" ]]; then
	split_image_ref "${console_image}" "console"
fi
if [[ -n "${metric_observer_image}" ]]; then
	split_image_ref "${metric_observer_image}" "metric-observer"
fi
if [[ -n "${workload_observer_image}" ]]; then
	split_image_ref "${workload_observer_image}" "workload-observer"
fi
if [[ -n "${serving_entrypoint_image}" ]]; then
	split_image_ref "${serving_entrypoint_image}" "serving-entrypoint"
fi
if [[ -n "${dashboard_image}" ]]; then
	split_image_ref "${dashboard_image}" "dashboard"
fi

if [[ -n "${extra_helm_set}" ]]; then
	helm_set="${extra_helm_set} --set operator.image.pullPolicy=${pull_policy} --set nodeAgent.image.pullPolicy=${pull_policy} --set profiler.image.pullPolicy=${pull_policy} --set console.image.pullPolicy=${pull_policy}"
else
	helm_set="--set operator.image.pullPolicy=${pull_policy} --set nodeAgent.image.pullPolicy=${pull_policy} --set profiler.image.pullPolicy=${pull_policy} --set console.image.pullPolicy=${pull_policy}"
fi
if [[ -n "${console_image}" ]]; then
	helm_set="${helm_set} --set console.enabled=true"
fi
helm_set="${helm_set} --set servingEntrypoint.image.pullPolicy=${pull_policy} --set dashboard.image.pullPolicy=${pull_policy}"
if [[ -n "${dashboard_image}" ]]; then
	helm_set="${helm_set} --set dashboard.enabled=true"
fi

if [[ "${push_mode}" == "ctr-plain-http" ]] ||
	{ [[ "${push_mode}" == "registry-direct" ]] && is_true "${direct_push_fallback}"; }; then
	require_cmd ctr
fi

case "${push_mode}" in
docker|buildx|registry-direct|ctr-plain-http|none) ;;
*)
	echo "unsupported --push-mode: ${push_mode}" >&2
	exit 1
	;;
esac

if is_true "${skip_push}" && ! is_true "${skip_build}" &&
	[[ "${push_mode}" == "buildx" || "${push_mode}" == "registry-direct" || "${push_mode}" == "ctr-plain-http" ]]; then
	echo "--skip-push cannot be combined with push mode ${push_mode}; use --push-mode none for a local-only build" >&2
	exit 1
fi

echo "==> fast-deploy configuration"
echo "release: ${release}"
echo "namespace: ${release_namespace}"
echo "chart: ${chart}"
echo "operator image: ${operator_image}"
echo "node-agent image: ${node_agent_image}"
echo "profiler image: ${profiler_image:-chart default}"
echo "console image: ${console_image:-disabled}"
echo "metric-observer image: ${metric_observer_image:-not selected}"
echo "workload-observer image: ${workload_observer_image:-not selected}"
echo "serving-entrypoint image: ${serving_entrypoint_image:-not selected}"
echo "dashboard image: ${dashboard_image:-not selected}"
echo "platforms: ${platforms}"
echo "components: ${components}"
if [[ -n "${buildx_builder}" ]]; then
	echo "buildx builder: ${buildx_builder}"
fi
echo "push mode: ${push_mode}"
echo "pull policy: ${pull_policy}"
echo "observation: ${observe_mode}"

if is_true "${auto_dirty_tag}"; then
	echo "dirty worktree: using unique image tag ${image_tag}" >&2
elif [[ -n "$(git status --porcelain)" ]]; then
	echo "warning: git worktree is dirty; ${image_tag} may not uniquely identify the image contents" >&2
fi

echo "==> target"
try "${kubectl_bin}" config current-context
try "${kubectl_bin}" cluster-info
try "${helm_bin}" status "${release}" --namespace "${release_namespace}"
try "${kubectl_bin}" get crd chillsystems.edge.dacs.io

wait_for_daemonset_object() {
	local attempt
	if is_true "${dry_run}"; then
		return
	fi
	echo "==> wait-node-agent-daemonset"
	for (( attempt = 1; attempt <= observe_attempts; attempt++ )); do
		if "${kubectl_bin}" -n "${release_namespace}" get daemonset \
			-l "${node_agent_selector}" \
			-o name 2>/dev/null | grep -q .; then
			return
		fi
		sleep 2
	done
	echo "node-agent DaemonSet did not appear after $((observe_attempts * 2))s" >&2
	return 1
}

buildx_args() {
	if [[ -n "${buildx_builder}" ]]; then
		printf '%s\n' "--builder" "${buildx_builder}"
	fi
}

docker_build_one() {
	local dockerfile="$1"
	local image="$2"
	run "${container_tool}" build --file "${dockerfile}" --tag "${image}" .
}

docker_push_one() {
	local image="$2"
	run "${container_tool}" push "${image}"
}

buildx_push_one() {
	local dockerfile="$1"
	local image="$2"
	local args=()
	while IFS= read -r arg; do
		[[ -n "${arg}" ]] && args+=("${arg}")
	done < <(buildx_args)

	run "${container_tool}" buildx build \
		"${args[@]}" \
		--push \
		--platform="${platforms}" \
		--file "${dockerfile}" \
		--tag "${image}" \
		.
}

registry_direct_build_push_one() {
	local dockerfile="$1"
	local image="$2"
	local args=()
	while IFS= read -r arg; do
		[[ -n "${arg}" ]] && args+=("${arg}")
	done < <(buildx_args)

	run "${container_tool}" buildx build \
		"${args[@]}" \
		--platform="${platforms}" \
		--file "${dockerfile}" \
		--tag "${image}" \
		--output "type=registry,registry.insecure=true" \
		.
}

ctr_plain_http_build_push_one() (
	set -e
	local name="$1"
	local dockerfile="$2"
	local image="$3"
	local tmpdir archive
	local args=()
	while IFS= read -r arg; do
		[[ -n "${arg}" ]] && args+=("${arg}")
	done < <(buildx_args)
	tmpdir="$(mktemp -d)"
	archive="${tmpdir}/${name}.oci.tar"
	trap 'rm -rf "${tmpdir}"' EXIT

	run "${container_tool}" buildx build \
		"${args[@]}" \
		--platform="${platforms}" \
		--file "${dockerfile}" \
		--tag "${image}" \
		--output "type=oci,dest=${archive}" \
		.

	# shellcheck disable=SC2206
	local ctr_cmd=(${CHILL_FAST_CTR:-sudo -n ctr})
	run "${ctr_cmd[@]}" -n "${containerd_namespace}" images import \
		--all-platforms \
		--digests \
		--index-name "${image}" \
		"${archive}"
	run "${ctr_cmd[@]}" -n "${containerd_namespace}" images push \
		--plain-http \
		"${image}"
)

run_selected_components() {
	local function_name="$1"
	local pids=()
	local names=()
	local failed="false"

	if component_selected operator; then
		"${function_name}" "build/docker/operator.Dockerfile" "${operator_image}" &
		pids+=("$!")
		names+=("operator")
	fi
	if component_selected node-agent; then
		"${function_name}" "build/docker/node-agent.Dockerfile" "${node_agent_image}" &
		pids+=("$!")
		names+=("node-agent")
	fi
	if component_selected profiler; then
		"${function_name}" "build/docker/profiler.Dockerfile" "${profiler_image}" &
		pids+=("$!")
		names+=("profiler")
	fi
	if component_selected console; then
		"${function_name}" "build/docker/console.Dockerfile" "${console_image}" &
		pids+=("$!")
		names+=("console")
	fi
	if component_selected metric-observer; then
		"${function_name}" "build/docker/metric-observer.Dockerfile" "${metric_observer_image}" &
		pids+=("$!")
		names+=("metric-observer")
	fi
	if component_selected workload-observer; then
		"${function_name}" "build/docker/workload-observer.Dockerfile" "${workload_observer_image}" &
		pids+=("$!")
		names+=("workload-observer")
	fi
	if component_selected serving-entrypoint; then
		"${function_name}" "build/docker/chill-server.Dockerfile" "${serving_entrypoint_image}" &
		pids+=("$!")
		names+=("serving-entrypoint")
	fi
	if component_selected dashboard; then
		"${function_name}" "build/docker/dashboard.Dockerfile" "${dashboard_image}" &
		pids+=("$!")
		names+=("dashboard")
	fi

	for i in "${!pids[@]}"; do
		if ! wait "${pids[$i]}"; then
			echo "${names[$i]} build/push failed" >&2
			failed="true"
		fi
	done
	! is_true "${failed}"
}

run_ctr_plain_http_components() {
	local pids=()
	local names=()
	local failed="false"

	if component_selected operator; then
		ctr_plain_http_build_push_one operator \
			"build/docker/operator.Dockerfile" "${operator_image}" &
		pids+=("$!")
		names+=("operator")
	fi
	if component_selected node-agent; then
		ctr_plain_http_build_push_one node-agent \
			"build/docker/node-agent.Dockerfile" "${node_agent_image}" &
		pids+=("$!")
		names+=("node-agent")
	fi
	if component_selected profiler; then
		ctr_plain_http_build_push_one profiler \
			"build/docker/profiler.Dockerfile" "${profiler_image}" &
		pids+=("$!")
		names+=("profiler")
	fi
	if component_selected console; then
		ctr_plain_http_build_push_one console \
			"build/docker/console.Dockerfile" "${console_image}" &
		pids+=("$!")
		names+=("console")
	fi
	if component_selected metric-observer; then
		ctr_plain_http_build_push_one metric-observer \
			"build/docker/metric-observer.Dockerfile" "${metric_observer_image}" &
		pids+=("$!")
		names+=("metric-observer")
	fi
	if component_selected workload-observer; then
		ctr_plain_http_build_push_one workload-observer \
			"build/docker/workload-observer.Dockerfile" "${workload_observer_image}" &
		pids+=("$!")
		names+=("workload-observer")
	fi
	if component_selected serving-entrypoint; then
		ctr_plain_http_build_push_one serving-entrypoint \
			"build/docker/chill-server.Dockerfile" "${serving_entrypoint_image}" &
		pids+=("$!")
		names+=("serving-entrypoint")
	fi
	if component_selected dashboard; then
		ctr_plain_http_build_push_one dashboard \
			"build/docker/dashboard.Dockerfile" "${dashboard_image}" &
		pids+=("$!")
		names+=("dashboard")
	fi

	for i in "${!pids[@]}"; do
		if ! wait "${pids[$i]}"; then
			echo "${names[$i]} ctr fallback failed" >&2
			failed="true"
		fi
	done
	! is_true "${failed}"
}

if ! is_true "${skip_build}"; then
	case "${push_mode}" in
	docker|none)
		echo "==> build"
		run_selected_components docker_build_one
		;;
	buildx)
		echo "==> build-and-push"
		run_selected_components buildx_push_one
		skip_push="true"
		;;
	registry-direct)
		echo "==> direct-build-and-push"
		if ! run_selected_components registry_direct_build_push_one; then
			if ! is_true "${direct_push_fallback}"; then
				echo "direct registry push failed and fallback is disabled" >&2
				exit 1
			fi
			echo "==> direct push failed; falling back to ctr-plain-http" >&2
			run_ctr_plain_http_components
		fi
		skip_push="true"
		;;
	ctr-plain-http)
		echo "==> build-and-push"
		run_ctr_plain_http_components
		skip_push="true"
		;;
	esac
fi

if ! is_true "${skip_push}"; then
	case "${push_mode}" in
	docker)
		echo "==> push"
		run_selected_components docker_push_one
		;;
	none)
		echo "skip push: push mode is none"
		;;
	buildx|registry-direct|ctr-plain-http)
		echo "skip push: push already happened during build"
		;;
	esac
fi

if ! is_true "${skip_deploy}"; then
	if ! is_true "${yes}" && ! is_true "${dry_run}"; then
		if [[ ! -t 0 ]]; then
			echo "refusing destructive deploy without --yes because stdin is not interactive" >&2
			exit 1
		fi
		echo
		echo "This will delete and reinstall Helm release ${release} in namespace ${release_namespace}."
		read -r -p "Type '${release}/${release_namespace}' to continue: " answer
		if [[ "${answer}" != "${release}/${release_namespace}" ]]; then
			echo "aborted"
			exit 1
		fi
	fi

	flow_env=(
		"HELM=${helm_bin}"
		"KUBECTL=${kubectl_bin}"
		"HELM_RELEASE=${release}"
		"HELM_NAMESPACE=${release_namespace}"
		"HELM_CHART=${chart}"
		"HELM_TIMEOUT=${timeout}"
		"HELM_VALUES=${values_file}"
		"HELM_SET=${helm_set}"
		"OPERATOR_IMG=${operator_image}"
		"NODE_AGENT_IMG=${node_agent_image}"
		"PROFILER_IMG=${profiler_image}"
		"CONSOLE_IMG=${console_image}"
		"SERVING_ENTRYPOINT_IMG=${serving_entrypoint_image}"
		"DASHBOARD_IMG=${dashboard_image}"
	)
	echo "==> preflight before uninstall"
	run env "${flow_env[@]}" ./hack/helm-release-flow.sh preflight

	echo "==> uninstall"
	run env "${flow_env[@]}" ./hack/helm-release-flow.sh uninstall

	echo "==> install"
	run env "${flow_env[@]}" "HELM_SKIP_PREFLIGHT=1" ./hack/helm-release-flow.sh install

	run "${kubectl_bin}" -n "${release_namespace}" rollout status deployment \
		-l "${operator_selector}" "--timeout=${timeout}"
	wait_for_daemonset_object
	run "${kubectl_bin}" -n "${release_namespace}" rollout status daemonset \
		-l "${node_agent_selector}" "--timeout=${timeout}"
	if "${kubectl_bin}" -n "${release_namespace}" get deployment -l "${profiler_selector}" -o name 2>/dev/null | grep -q .; then
		run "${kubectl_bin}" -n "${release_namespace}" rollout status deployment \
			-l "${profiler_selector}" "--timeout=${timeout}"
	fi
	if "${kubectl_bin}" -n "${release_namespace}" get daemonset -l "${node_agent_actuation_selector}" -o name 2>/dev/null | grep -q .; then
		run "${kubectl_bin}" -n "${release_namespace}" rollout status daemonset \
			-l "${node_agent_actuation_selector}" "--timeout=${timeout}"
	fi
	if "${kubectl_bin}" -n "${release_namespace}" get deployment -l "${console_selector}" -o name 2>/dev/null | grep -q .; then
		run "${kubectl_bin}" -n "${release_namespace}" rollout status deployment \
			-l "${console_selector}" "--timeout=${timeout}"
	fi
	if "${kubectl_bin}" -n "${release_namespace}" get deployment -l "${serving_entrypoint_selector}" -o name 2>/dev/null | grep -q .; then
		run "${kubectl_bin}" -n "${release_namespace}" rollout status deployment \
			-l "${serving_entrypoint_selector}" "--timeout=${timeout}"
	fi
	if "${kubectl_bin}" -n "${release_namespace}" get deployment -l "${dashboard_selector}" -o name 2>/dev/null | grep -q .; then
		run "${kubectl_bin}" -n "${release_namespace}" rollout status deployment \
			-l "${dashboard_selector}" "--timeout=${timeout}"
	fi
fi

echo "==> observation"
echo "commit: $(git rev-parse --short HEAD 2>/dev/null || printf unknown)"
echo "operator image: ${operator_image}"
echo "node-agent image: ${node_agent_image}"
echo "profiler image: ${profiler_image:-chart default}"
echo "console image: ${console_image:-disabled}"
echo "metric-observer image: ${metric_observer_image:-not selected}"
echo "workload-observer image: ${workload_observer_image:-not selected}"
echo "serving-entrypoint image: ${serving_entrypoint_image:-not selected}"
echo "dashboard image: ${dashboard_image:-not selected}"
try "${helm_bin}" status "${release}" --namespace "${release_namespace}"
try "${kubectl_bin}" get chillsystems.edge.dacs.io -A -o wide
try "${kubectl_bin}" -n "${release_namespace}" get deployments,daemonsets,pods -o wide
try "${kubectl_bin}" -n "${release_namespace}" get pods \
	-o 'custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,IMAGE:.spec.containers[*].image,IMAGE_ID:.status.containerStatuses[*].imageID'
try "${kubectl_bin}" -n "${release_namespace}" rollout status deployment -l "${operator_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" rollout status daemonset -l "${node_agent_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" rollout status daemonset -l "${node_agent_actuation_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" rollout status deployment -l "${profiler_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" rollout status deployment -l "${console_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" rollout status deployment -l "${serving_entrypoint_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" rollout status deployment -l "${dashboard_selector}" "--timeout=${timeout}"
try "${kubectl_bin}" -n "${release_namespace}" get events \
	--field-selector type=Warning --sort-by=.lastTimestamp
if [[ "${observe_mode}" == "full" ]]; then
	try "${kubectl_bin}" -n "${release_namespace}" get events --sort-by=.lastTimestamp
	try "${kubectl_bin}" -n "${release_namespace}" logs -l "${operator_selector}" --all-containers=true --tail=120 --prefix
	try "${kubectl_bin}" -n "${release_namespace}" logs -l "${node_agent_selector}" --all-containers=true --tail=120 --prefix
	try "${kubectl_bin}" -n "${release_namespace}" logs -l "${node_agent_actuation_selector}" --all-containers=true --tail=120 --prefix
	try "${kubectl_bin}" -n "${release_namespace}" logs -l "${profiler_selector}" --all-containers=true --tail=120 --prefix
	try "${kubectl_bin}" -n "${release_namespace}" logs -l "${console_selector}" --all-containers=true --tail=120 --prefix
	try "${kubectl_bin}" -n "${release_namespace}" logs -l "${serving_entrypoint_selector}" --all-containers=true --tail=120 --prefix
	try "${kubectl_bin}" -n "${release_namespace}" logs -l "${dashboard_selector}" --all-containers=true --tail=120 --prefix
fi

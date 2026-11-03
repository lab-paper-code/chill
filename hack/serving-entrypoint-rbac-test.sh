#!/usr/bin/env bash
# Renders the chart and checks the ChillServer grants measurement admission uses:
# Service list in the serving namespace (hand-back finds runner Services) and
# patch of the measurements ChillServer runs and their status. Also check the
# dashboard caller's serving-manager grants against the measurement handler SARs.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
helm="${HELM:-helm}"
rendered="$(mktemp)"
trap 'rm -f "${rendered}"' EXIT

check() {
	local expected_caller="$1"
	local expected_reader="$2"
	shift 2
	"${helm}" template chill "${repo_root}/charts/chill" --namespace chill-system "$@" >"${rendered}"
	python3 - "${rendered}" "${expected_caller}" "${expected_reader}" <<'PY'
import sys, yaml
documents = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
identities = [(d["kind"], d["metadata"].get("namespace", ""), d["metadata"]["name"]) for d in documents]
assert len(identities) == len(set(identities)), "duplicate rendered (kind, namespace, name)"
role_objects = {(d["kind"], d["metadata"].get("namespace", ""), d["metadata"]["name"]): d
                for d in documents if d["kind"] in ("Role", "ClusterRole")}

def effective(account):
    resources, urls = set(), set()
    for binding in documents:
        if binding["kind"] not in ("RoleBinding", "ClusterRoleBinding"):
            continue
        if {"kind": "ServiceAccount", "name": account, "namespace": "chill-system"} not in binding.get("subjects", []):
            continue
        ref = binding["roleRef"]
        namespace = binding["metadata"].get("namespace", "") if ref["kind"] == "Role" else ""
        role = role_objects[(ref["kind"], namespace, ref["name"])]
        scope = binding["metadata"]["namespace"] if binding["kind"] == "RoleBinding" else "*"
        for rule in role.get("rules", []):
            for verb in rule["verbs"]:
                urls.update((verb, url) for url in rule.get("nonResourceURLs", []))
                for group in rule.get("apiGroups", []):
                    resources.update((scope, group, resource, verb, tuple(rule.get("resourceNames", [])))
                                     for resource in rule.get("resources", []))
    return resources, urls

reader = sys.argv[3]
reader_objects = [d for d in documents if d["metadata"]["name"] in
                  {"chill-dashboard-reader", "chill-dashboard-reader-namespaced"}]
if reader:
    assert any(d["kind"] == "ServiceAccount" and d["metadata"]["name"] == reader
               and d["metadata"]["namespace"] == "chill-system" for d in documents)
    reader_resources, reader_urls = effective(reader)
    assert reader_objects and reader_urls
    assert all(verb == "get" for verb, _ in reader_urls), "reader acquired HTTP writes"
    assert all(row[3] in {"get", "list", "watch"} for row in reader_resources), "reader acquired native writes"
    assert not any(row[2] == "secrets" for row in reader_resources)
    manager = next(d for d in documents if d["kind"] == "ClusterRole"
                   and d["metadata"]["name"] == "chill-serving-manager")
    expected = {("get", path) for rule in manager["rules"] if "get" in rule["verbs"]
                for path in rule.get("nonResourceURLs", [])}
    assert reader_urls == expected, (reader_urls - expected, expected - reader_urls)
    assert not any("/observations/raw" in path or "catalog-read" in path for _, path in reader_urls)
    namespaces = {d["metadata"]["namespace"] for d in documents if d["kind"] == "Role"
                  and d["metadata"]["name"] == "chill-serving-manager"}
    for namespace in namespaces:
        for resource in ("profilingruns", "batchenergyoptimizations", "operationcostmeasurements",
                         "dispatchoccupancymeasurements"):
            for verb in ("get", "list"):
                assert (namespace, "edge.dacs.io", resource, verb, ()) in reader_resources
        evaluation_enabled = any(d["kind"] == "Role" and d["metadata"]["name"] == "chill-serving-manager"
                                 and any("evaluationruns" in r.get("resources", []) for r in d["rules"])
                                 for d in documents)
        for resource in ("evaluationruns", "loadscenarios", "evaluationstages"):
            for verb in ("get", "list", "watch"):
                assert ((namespace, "edge.dacs.io", resource, verb, ()) in reader_resources) == evaluation_enabled
else:
    assert not reader_objects, "disabled reader rendered RBAC"

dashboard_bindings = [d for d in documents if d["kind"] == "ClusterRoleBinding"
                      and d["metadata"]["name"] == "chill-dashboard-evaluation-operator"]
caller = sys.argv[2]
if caller:
    assert len(dashboard_bindings) == 1, "dashboard evaluation opt-in binding is missing"
    binding = dashboard_bindings[0]
    assert binding["roleRef"] == {"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole",
                                  "name": "chill-evaluation-operator"}
    assert binding["subjects"] == [{"kind": "ServiceAccount", "name": caller, "namespace": "chill-system"}], \
        "dashboard caller must stay in the release namespace"
    expected_urls = {(verb, path) for verb in ("post", "put", "delete")
                     for path in ("/chill/api/v1/evaluations", "/chill/api/v1/evaluations/*")}
    assert effective(caller) == (set(), expected_urls), \
        "dashboard opt-in must grant evaluation mutations only, not scheduler editing or native resources"
    assert not any(d["kind"] == "ServiceAccount" and d["metadata"]["name"] == caller for d in documents), \
        "dashboard opt-in must not create the external caller SA"
else:
    assert not dashboard_bindings, "empty dashboard caller unexpectedly acquired an evaluation binding"

config = next((d for d in documents if d["kind"] == "ConfigMap" and d["metadata"]["name"] == "chill-evaluation-config"), None)
if config:
    account = config["data"]["actionServiceAccount"]
    resources, urls = effective(account)
    expected = {
        ("chill-system", "batch", "jobs", verb, ()) for verb in ("create", "get", "list", "delete")
    } | {
        ("chill-system", "", "pods", "get", ()),
        ("chill-system", "", "pods/log", "get", ()),
        ("chill-system", "", "pods/exec", "create", ()),
        ("chill-system", "edge.dacs.io", "loadscenarios", "get", ()),
        ("chill-system", "apps", "deployments", "get", ()),
        ("*", "", "nodes", "get", ()),
        ("*", "", "pods", "list", ()),
    } | {
        ("*", "edge.dacs.io", resource, "get", ()) for resource in ("workloads", "datasets", "preprocessors")
    }
    settings_binding = next((d for d in documents if d["kind"] == "RoleBinding"
                             and d["metadata"]["name"] == "chill-evaluation-settings-writer"), None)
    if settings_binding:
        expected.add((settings_binding["metadata"]["namespace"], "", "configmaps", "update", ("chill-scheduler",)))
    assert resources == expected, ("evaluation action resource permissions differ", resources ^ expected)
    expected_urls = {("get", "/chill/api/v1/" + path) for path in (
        "workloads", "workloads/*", "scheduler/settings", "observations/power", "observations/raw", "observations/raw/*"
    )} | {("put", "/chill/api/v1/scheduler/settings")}
    assert urls == expected_urls, ("evaluation action public URLs differ", urls ^ expected_urls)
    assert effective(config["data"]["publishServiceAccount"]) == (set(), set()), "publisher acquired API rights"
    obsolete = {"chill-evaluation-action-evidence-collector", "chill-evaluation-action-scheduler-editor"}
    assert not any(d["kind"] == "ClusterRoleBinding" and d["metadata"]["name"] in obsolete for d in documents)
    if any(d["kind"] == "ServiceAccount" and d["metadata"]["name"] == "chill-profiler" for d in documents):
        controller_resources, controller_urls = effective("chill-profiler")
        assert not controller_urls, "controller retained internal HTTP route permissions"
        for verb in ("get", "list", "patch"):
            assert ("*", "", "nodes", verb, ()) in controller_resources, "fault controller lost Node " + verb
assert not any(d["kind"] in ("ServiceAccount", "ClusterRoleBinding")
               and d["metadata"]["name"] == "chill-evaluation-evidence-collector" for d in documents), \
    "unused standalone evaluation collector identity remains"
roles = [d for d in documents if d.get("kind") == "Role"
         and d["metadata"]["name"].startswith("chill-serving-entrypoint")]
if not roles:
    sys.exit(0)

def grants(role, resource, verb):
    return any(resource in r.get("resources", []) and verb in r.get("verbs", []) for r in role.get("rules", []))

query = next(d for d in documents if d.get("kind") == "ClusterRole"
               and d["metadata"]["name"] == "chill-query-preflight-reader")
for kind in ("modelspecs", "deviceclasses", "preprocessors", "datasets"):
    if not grants(query, kind, "get"):
        sys.exit("query preflight lacks cluster-scope " + kind + " get")
for kind in ("preprocessors", "datasets"):
    rules = [r for r in query["rules"] if kind in r.get("resources", [])]
    verbs = set().union(*(set(r["verbs"]) for r in rules))
    if verbs != {"get", "list"}:
        sys.exit("query inputs require get/list, never writes: " + kind)
catalog = next(d for d in documents if d.get("kind") == "ClusterRole"
               and d["metadata"]["name"] == "chill-query-catalog-reader")
if catalog["rules"] != [
    {"nonResourceURLs": ["/chill/api/v1/scheduler/catalog-read"], "verbs": ["get"]},
    {"nonResourceURLs": ["/chill/api/v1/observations/occupancy"], "verbs": ["get"]},
]:
    sys.exit("query owner read grants differ")
# Prove the rendered owner-read binding, not external bootstrap roles such as
# system:auth-delegator (which Helm intentionally does not render).
assert any(d["kind"] == "ClusterRoleBinding" and
           d["roleRef"]["kind"] == "ClusterRole" and
           d["roleRef"]["name"] == catalog["metadata"]["name"] and
           {"kind": "ServiceAccount", "name": "chill-query", "namespace": "chill-system"} in d.get("subjects", [])
           for d in documents), "query SA lacks occupancy GET binding"

serving = [r for r in roles if r["metadata"]["name"] == "chill-serving-entrypoint"]
if not serving or not grants(serving[0], "services", "list"):
    sys.exit("serving namespace Role lacks services list")
if not grants(serving[0], "deployments", "get"):
    sys.exit("serving namespace Role lacks target Deployment get")
profiles = [r for r in roles if r["metadata"]["name"] == "chill-serving-entrypoint-profiles"]
if not profiles:
    sys.exit("profile namespace Role is missing")
for kind in ("operationcostmeasurements", "dispatchoccupancymeasurements"):
    if not all(grants(r, kind, "patch") and grants(r, kind + "/status", "patch") for r in profiles):
        sys.exit("a profile namespace Role lacks " + kind + " and status patch")
# occupancy_service.go / BEO create and delete natively as the server's own SA.
for kind in ("batchenergyoptimizations", "dispatchoccupancymeasurements"):
    for verb in ("get", "list", "create", "delete"):
        if not all(grants(r, kind, verb) for r in profiles):
            sys.exit("server SA profile Role lacks " + verb + " " + kind)

# measurements.go requires create (collection) / delete (named) as the caller.
# authorizer.go separately checks the lowercase HTTP verb and /chill-prefixed path.
# Check both rendered grants; owner-SA rights never substitute for caller rights.
manager = next(d for d in documents if d.get("kind") == "ClusterRole"
               and d["metadata"]["name"] == "chill-serving-manager")
callers = [d for d in documents if d.get("kind") == "Role"
           and d["metadata"]["name"] == "chill-serving-manager"]
assert {r["metadata"]["namespace"] for r in callers} == {r["metadata"]["namespace"] for r in profiles}, \
    "serving-manager caller Roles do not cover every allowed profile namespace"

def caller_path_grants(path, verb):
    return any(verb in rule.get("verbs", []) and
               any(url == path or url.endswith("*") and path.startswith(url[:-1])
                   for url in rule.get("nonResourceURLs", []))
               for rule in manager.get("rules", []))
assert caller_path_grants("/chill/api/v1/observations/occupancy", "get"), "dashboard lacks occupancy GET"
assert not caller_path_grants("/chill/api/v1/observations/occupancy", "post"), "dashboard acquired occupancy write"

def caller_resource_grants(role, resource, verb, name=""):
    return any("edge.dacs.io" in rule.get("apiGroups", []) and
               resource in rule.get("resources", []) and verb in rule.get("verbs", []) and
               (not rule.get("resourceNames") or name in rule["resourceNames"])
               for rule in role.get("rules", []))

for role in callers:
    namespace = role["metadata"]["namespace"]
    prefix = "/chill/api/v1/namespaces/" + namespace + "/"
    for collection, resource in (("batch-energy-optimizations", "batchenergyoptimizations"),
                                 ("dispatch-occupancy-measurements", "dispatchoccupancymeasurements")):
        for http_verb, resource_verb, name in (("post", "create", ""), ("delete", "delete", "native-task")):
            path = prefix + collection + ("/" + name if name else "")
            assert caller_path_grants(path, http_verb), \
                "dashboard serving-manager lacks nonResource SAR: " + http_verb + " " + path
            assert caller_resource_grants(role, resource, resource_verb, name), \
                "dashboard serving-manager lacks resource SAR: " + namespace + " " + resource_verb + " " + resource
        for verb in ("get", "list"):
            assert caller_resource_grants(role, resource, verb), \
                "dashboard serving-manager lost native read grant: " + namespace + " " + verb + " " + resource
    assert caller_path_grants(prefix + "batch-energy-optimizations/preflight", "post"), \
        "dashboard serving-manager lost BEO preflight POST"
PY
}

# ChillServer is opt-in; render it in both profiles with the scheduler configured.
serving=(--set servingEntrypoint.enabled=true --set query.enabled=true --set servingEntrypoint.tls.secretName=chill-tls
	--set evidenceStore.connection.bucket=chill --set evidenceStore.connection.region=local
	--set servingEntrypoint.scheduler.configMap=chill-scheduler)
check "" ""
check "" "" -f "${repo_root}/charts/chill/values-profiler.yaml"
check "" "" -f "${repo_root}/charts/chill/values-profiler.yaml" --set evaluation.enabled=true
check "" "" "${serving[@]}"
check "" "" "${serving[@]}" --set evaluation.enabled=true
check "" "" -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set 'servingEntrypoint.profileNamespaces={chill-system,measurements}'
check "" "" -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set evaluation.enabled=true
check chill-dashboard-user "" "${serving[@]}" --set dashboard.callerServiceAccount=chill-dashboard-user
check chill-dashboard-user "" -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set evaluation.enabled=true --set dashboard.enabled=true --set servingEntrypoint.namespace=serving \
	--set dashboard.callerServiceAccount=chill-dashboard-user
check "" chill-dashboard-reader "${serving[@]}" --set dashboard.reader.enabled=true
check chill-dashboard-user chill-dashboard-reader -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set evaluation.enabled=true --set servingEntrypoint.namespace=serving \
	--set 'servingEntrypoint.profileNamespaces={serving,measurements}' \
	--set dashboard.callerServiceAccount=chill-dashboard-user --set dashboard.reader.enabled=true

reject_dashboard() {
	local key="$1"
	shift
	if "${helm}" template chill "${repo_root}/charts/chill" --namespace chill-system "$@" >"${rendered}" 2>&1; then
		echo "invalid ${key} configuration unexpectedly rendered" >&2
		exit 1
	fi
	[[ "$(<"${rendered}")" == *"${key}"* ]]
}
reject_dashboard dashboard.reader.enabled --set dashboard.reader.enabled=true
reject_dashboard dashboard.reader.enabled "${serving[@]}" --set-string dashboard.reader.enabled=true
reject_dashboard dashboard.callerServiceAccount --set dashboard.callerServiceAccount=chill-dashboard-user
reject_dashboard dashboard.callerServiceAccount "${serving[@]}" --set dashboard.callerServiceAccount=true
reject_dashboard dashboard.callerServiceAccount "${serving[@]}" --set dashboard.callerServiceAccount=other-namespace/chill-dashboard-user
reject_dashboard dashboard.callerServiceAccount "${serving[@]}" --set dashboard.reader.enabled=true \
	--set dashboard.callerServiceAccount=chill-dashboard-reader
echo "serving entrypoint RBAC: pass"

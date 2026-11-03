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
	"${helm}" template chill "${repo_root}/charts/chill" --namespace chill-system "$@" >"${rendered}"
	python3 - "${rendered}" <<'PY'
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
        ("chill-system", "apps", "replicasets", "get", ()),
        ("chill-system", "apps", "statefulsets", "get", ()),
        ("chill-system", "batch", "cronjobs", "get", ()),
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
if catalog["rules"] != [{"nonResourceURLs": ["/chill/api/v1/scheduler/catalog-read"], "verbs": ["get"]}]:
    sys.exit("query catalog grant is not the exact owner GET")

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
check
check -f "${repo_root}/charts/chill/values-profiler.yaml"
check -f "${repo_root}/charts/chill/values-profiler.yaml" --set evaluation.enabled=true
check "${serving[@]}"
check "${serving[@]}" --set evaluation.enabled=true
check -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set 'servingEntrypoint.profileNamespaces={chill-system,measurements}'
check -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set evaluation.enabled=true
echo "serving entrypoint RBAC: pass"

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
if any(d["kind"] == "ServiceAccount" and d["metadata"]["name"] == "chill-evaluation-action" for d in documents):
    for name, subject in (("chill-evaluation-action-evidence-collector", "chill-evaluation-action"),
                          ("chill-evaluation-evidence-collector", "chill-evaluation-evidence-collector")):
        binding = next(d for d in documents if d["kind"] == "ClusterRoleBinding" and d["metadata"]["name"] == name)
        assert binding["roleRef"]["name"] == "chill-evidence-collector"
        assert binding["subjects"] == [{"kind": "ServiceAccount", "name": subject, "namespace": "chill-system"}]
roles = [d for d in documents if d.get("kind") == "Role"
         and d["metadata"]["name"].startswith("chill-serving-entrypoint")]

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
check "${serving[@]}"
check -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set 'servingEntrypoint.profileNamespaces={chill-system,measurements}'
check -f "${repo_root}/charts/chill/values-profiler.yaml" "${serving[@]}" \
	--set evaluation.enabled=true --set servingEntrypoint.evidenceCollector.enabled=true
echo "serving entrypoint RBAC: pass"

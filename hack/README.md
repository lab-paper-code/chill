# Hack Scripts

Use a repo-local kind kubeconfig for development and e2e tests:

```sh
./hack/kind-up.sh
export KUBECONFIG="$PWD/.kube/chill-kind.config"
```

The tracked `.envrc` points `KUBECONFIG` at `.kube/chill-kind.config` when direnv is enabled.

Do not use the testbed kubeconfig as the default shell context inside this repo. Access the testbed only with an explicit prefix:

```sh
KUBECONFIG=/path/to/testbed.kubeconfig kubectl get nodes
```

Before installing the Helm chart with `crds.enabled=true` on a shared cluster,
check CRD ownership:

```sh
make helm-crd-check
```

Use `make helm-adopt-crds` only for an intentional release migration.

Helm install and cleanup is implemented behind the repo Make targets:

```sh
make helm-install
make helm-uninstall
```

`make helm-uninstall` treats runtime cleanup as CHILL's responsibility: it
deletes the root `ChillSystem` and waits for finalizers before calling Helm
with hooks disabled. The chart hook remains only as a safety net for direct
`helm uninstall`.

`make helm-purge-crds` is intentionally guarded and requires
`CONFIRM_PURGE_CRDS=chill`.

## CPU ORT runtime declarations

The runtime declaration producer selects one runnable child from a captured OCI
index and then executes that exact `repository@sha256` image. It does not accept
caller-supplied probe output. Capture the published index and run the producer
on a host that can execute the selected platform:

```sh
docker manifest inspect REPOSITORY@INDEX_DIGEST > /tmp/runtime-index.json
python3 hack/profiling/runtime_declaration.py \
  --repository REPOSITORY \
  --index /tmp/runtime-index.json \
  --architecture arm64 \
  --expected-runtime-version 1.23.2 \
  --output-dir /tmp/runtime-declarations
```

Use `docker manifest inspect --insecure` only for an explicitly trusted
plain-HTTP development registry. The digest-named declaration and retained raw
probe are immutable as a pair. The producer fails on an ambiguous child,
architecture drift, ORT version drift, or missing `CPUExecutionProvider`.

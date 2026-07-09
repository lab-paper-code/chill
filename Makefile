# Image URLs to use for building and pushing component images.
# IMG is kept as a controller-image alias for Kubebuilder scaffold compatibility.
IMG ?= chill/controller:latest
CONTROLLER_IMG ?= $(IMG)
NODE_DISCOVERY_IMG ?= chill/node-discovery:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.31.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker
BUILDX_BUILDER ?= chill-builder
HELM_RELEASE ?= chill
HELM_NAMESPACE ?= chill-system
HELM_CHART ?= charts/chill
HELM_TIMEOUT ?= 2m
HELM_VALUES ?=
HELM_SET ?=
HELM_FLOW = ./hack/helm-release-flow.sh
HELM_FLOW_ENV = HELM=$(HELM) KUBECTL=$(KUBECTL) KUBECONFORM=$(KUBECONFORM) KUBECONFORM_FLAGS="$(KUBECONFORM_FLAGS)" HELM_RELEASE=$(HELM_RELEASE) HELM_NAMESPACE=$(HELM_NAMESPACE) HELM_CHART=$(HELM_CHART) HELM_TIMEOUT=$(HELM_TIMEOUT) HELM_VALUES="$(HELM_VALUES)" HELM_SET="$(HELM_SET)" CONTROLLER_IMG="$(CONTROLLER_IMG)" NODE_DISCOVERY_IMG="$(NODE_DISCOVERY_IMG)"

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd paths="./..." output:crd:artifacts:config=config/crd/bases
	$(MAKE) helm-sync-crds
	$(MAKE) helm-sync-rbac

.PHONY: helm-sync-crds
helm-sync-crds: ## Sync generated CRDs into the Helm chart.
	./hack/sync-helm-crds.sh

.PHONY: helm-sync-rbac
helm-sync-rbac: ## Sync generated manager ClusterRole rules into the Helm chart.
	./hack/sync-helm-rbac.sh

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
.PHONY: test-e2e
test-e2e: ## Run e2e tests; requires the current kube context to be kind-*.
	go test ./test/e2e/ -v -ginkgo.v

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

##@ Helm

.PHONY: helm-lint
helm-lint: ## Run Helm chart lint.
	values_args=(); \
	if [ -n "$(HELM_VALUES)" ]; then values_args+=("-f" "$(HELM_VALUES)"); fi; \
	$(HELM) lint $(HELM_CHART) "$${values_args[@]}"

.PHONY: helm-template
helm-template: kubeconform ## Render and validate Helm chart.
	values_args=(); \
	if [ -n "$(HELM_VALUES)" ]; then values_args+=("-f" "$(HELM_VALUES)"); fi; \
	$(HELM) template $(HELM_RELEASE) $(HELM_CHART) --namespace $(HELM_NAMESPACE) "$${values_args[@]}" >/tmp/chill-helm.yaml
	$(KUBECONFORM) $(KUBECONFORM_FLAGS) /tmp/chill-helm.yaml

.PHONY: helm-crd-check
helm-crd-check: ## Check whether live CRDs can be managed by the Helm release.
	RELEASE_NAME=$(HELM_RELEASE) RELEASE_NAMESPACE=$(HELM_NAMESPACE) CRD_DIR=config/crd/bases KUBECTL=$(KUBECTL) ./hack/helm-crd-ownership.sh check

.PHONY: helm-adopt-crds
helm-adopt-crds: ## Adopt existing CRDs into the Helm release; use FROM_RELEASE_NAME/FROM_RELEASE_NAMESPACE for old Helm owners.
	RELEASE_NAME=$(HELM_RELEASE) RELEASE_NAMESPACE=$(HELM_NAMESPACE) CRD_DIR=config/crd/bases KUBECTL=$(KUBECTL) FROM_RELEASE_NAME="$(FROM_RELEASE_NAME)" FROM_RELEASE_NAMESPACE="$(FROM_RELEASE_NAMESPACE)" ./hack/helm-crd-ownership.sh adopt

.PHONY: helm-install-smoke
helm-install-smoke: ## Install or upgrade the chart without starting pods or managing CRDs.
	values_args=(); \
	if [ -n "$(HELM_VALUES)" ]; then values_args+=("-f" "$(HELM_VALUES)"); fi; \
	$(HELM) upgrade --install $(HELM_RELEASE) $(HELM_CHART) \
		--namespace $(HELM_NAMESPACE) \
		--create-namespace \
		"$${values_args[@]}" \
		--set crds.enabled=false \
		--set controller.replicaCount=0 \
		--set nodeDiscovery.enabled=false \
		$(HELM_SET) \
		--wait \
		--timeout $(HELM_TIMEOUT)

.PHONY: helm-preflight
helm-preflight: kubeconform ## Validate chart rendering and live CRD ownership.
	@$(HELM_FLOW_ENV) $(HELM_FLOW) preflight

.PHONY: helm-install
helm-install: kubeconform ## Install or upgrade CHILL without starting runtime pods.
	@$(HELM_FLOW_ENV) $(HELM_FLOW) install

.PHONY: helm-start
helm-start: ## Start the CHILL runtime components.
	@$(HELM_FLOW_ENV) $(HELM_FLOW) start

.PHONY: helm-stop
helm-stop: ## Stop CHILL runtime components.
	@$(HELM_FLOW_ENV) $(HELM_FLOW) stop

.PHONY: helm-uninstall
helm-uninstall: ## Stop CHILL and uninstall the Helm release while keeping CRDs.
	@$(HELM_FLOW_ENV) $(HELM_FLOW) uninstall

.PHONY: helm-purge-crds
helm-purge-crds: ## Delete CHILL CRDs; requires CONFIRM_PURGE_CRDS=$(HELM_RELEASE).
	@$(HELM_FLOW_ENV) CONFIRM_PURGE_CRDS="$(CONFIRM_PURGE_CRDS)" $(HELM_FLOW) purge-crds

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build controller and node-discovery binaries.
	go build -o bin/manager cmd/main.go
	go build -o bin/node-discovery ./cmd/node-discovery

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build images targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: docker-build-controller ## Build the controller image.

.PHONY: docker-build-controller
docker-build-controller: ## Build the controller image.
	$(CONTAINER_TOOL) build -f build/docker/controller.Dockerfile -t $(CONTROLLER_IMG) .

.PHONY: docker-build-node-discovery
docker-build-node-discovery: ## Build the node-discovery image.
	$(CONTAINER_TOOL) build -f build/docker/node-discovery.Dockerfile -t $(NODE_DISCOVERY_IMG) .

.PHONY: docker-build-all
docker-build-all: docker-build-controller docker-build-node-discovery ## Build all component images.

.PHONY: docker-push
docker-push: docker-push-controller ## Push the controller image.

.PHONY: docker-push-controller
docker-push-controller: ## Push the controller image.
	$(CONTAINER_TOOL) push $(CONTROLLER_IMG)

.PHONY: docker-push-node-discovery
docker-push-node-discovery: ## Push the node-discovery image.
	$(CONTAINER_TOOL) push $(NODE_DISCOVERY_IMG)

.PHONY: docker-push-all
docker-push-all: docker-push-controller docker-push-node-discovery ## Push all component images.

# PLATFORMS defines the target platforms for images built to provide support to multiple
# architectures. (i.e. make docker-buildx-all CONTROLLER_IMG=myregistry/chill/controller:0.0.1 NODE_DISCOVERY_IMG=myregistry/chill/node-discovery:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the images to your registry
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64
.PHONY: docker-buildx
docker-buildx: docker-buildx-controller ## Build and push the controller image for cross-platform support.

.PHONY: docker-buildx-controller
docker-buildx-controller: ## Build and push the controller image for cross-platform support.
	$(CONTAINER_TOOL) buildx inspect $(BUILDX_BUILDER) >/dev/null 2>&1 || $(CONTAINER_TOOL) buildx create --name $(BUILDX_BUILDER)
	$(CONTAINER_TOOL) buildx use $(BUILDX_BUILDER)
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) -f build/docker/controller.Dockerfile --tag $(CONTROLLER_IMG) .

.PHONY: docker-buildx-node-discovery
docker-buildx-node-discovery: ## Build and push the node-discovery image for cross-platform support.
	$(CONTAINER_TOOL) buildx inspect $(BUILDX_BUILDER) >/dev/null 2>&1 || $(CONTAINER_TOOL) buildx create --name $(BUILDX_BUILDER)
	$(CONTAINER_TOOL) buildx use $(BUILDX_BUILDER)
	$(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) -f build/docker/node-discovery.Dockerfile --tag $(NODE_DISCOVERY_IMG) .

.PHONY: docker-buildx-all
docker-buildx-all: docker-buildx-controller docker-buildx-node-discovery ## Build and push all component images for cross-platform support.

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(CONTROLLER_IMG)
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=$(CONTROLLER_IMG)
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
HELM ?= helm
KUBECONFORM ?= $(LOCALBIN)/kubeconform
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.4.3
CONTROLLER_TOOLS_VERSION ?= v0.16.1
ENVTEST_VERSION ?= release-0.19
GOLANGCI_LINT_VERSION ?= v1.64.5
KUBECONFORM_VERSION ?= v0.6.7
# kubeconform's Kubernetes schema catalog omits top-level CRD schemas.
KUBECONFORM_FLAGS ?= -strict -summary -kubernetes-version 1.31.0 -skip CustomResourceDefinition

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: kubeconform
kubeconform: $(KUBECONFORM) ## Download kubeconform locally if necessary.
$(KUBECONFORM): $(LOCALBIN)
	$(call go-install-tool,$(KUBECONFORM),github.com/yannh/kubeconform/cmd/kubeconform,$(KUBECONFORM_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef

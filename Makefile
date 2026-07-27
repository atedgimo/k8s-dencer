# k8s-dencer — single entry point for build, deploy, lint and demo.
#
# Everything runs in-cluster: there is no out-of-cluster dev shortcut. The Helm
# chart is the product, so the same chart is used locally and in production,
# separated only by a values overlay under charts/k8s-dencer/ci/.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help
.SHELLFLAGS := -eu -o pipefail -c

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

# orbstack | k3d | kind | minikube
# Selects both the images-load implementation and the ci/ values overlay.
# Nothing outside this file and charts/k8s-dencer/ci/ may assume a provider.
CLUSTER_PROVIDER ?= orbstack

NAMESPACE   ?= k8s-dencer
RELEASE     ?= k8s-dencer
CHART       ?= charts/k8s-dencer

# Local port for `make ui`. Not 8080: kagent's own UI port-forward commonly
# holds that, and the collision is silent enough to waste real time.
UI_PORT     ?= 8090
# Separate port so capturing a fixture never collides with `make ui`.
FIXTURE_PORT ?= 8091

# Demo fabric (POC only). Separate releases and namespaces from the product so
# the topology can be torn down without touching k8s-dencer.
KWOK_CHART_VERSION ?= 0.3.0
KWOK_NAMESPACE     ?= kwok
DEMO_NAMESPACE     ?= dencer-demo
DEMO_RELEASE       ?= dencer-demo
SCENARIO           ?= a-fragmented

# The tag must change whenever the *content* changes, not just the commit.
# A plain "-dirty" suffix is the same string for every uncommitted state, so
# two different builds at one SHA share a tag: the podspec does not change,
# helm upgrade reports success, and the pod quietly keeps the old image. The
# suffix therefore hashes the working tree — tracked diffs and untracked file
# contents both — so each distinct state gets its own tag and pods actually roll.
GIT_SHA     := $(shell git rev-parse --short HEAD 2>/dev/null || echo nogit)
DIRTY_HASH  := $(shell { git diff HEAD 2>/dev/null; \
                         git ls-files --others --exclude-standard -z 2>/dev/null \
                           | xargs -0 shasum 2>/dev/null; } | shasum | cut -c1-8)
GIT_DIRTY   := $(shell test -n "$$(git status --porcelain 2>/dev/null)" \
                 && echo "-dirty.$(DIRTY_HASH)" || true)
IMAGE_TAG   ?= $(GIT_SHA)$(GIT_DIRTY)

IMAGE_PREFIX ?= k8s-dencer
PLATFORMS    ?= linux/amd64,linux/arm64

# Pinned tool versions, installed into ./bin so a workstation without brew
# still gets a reproducible lint.
KUBECONFORM_VERSION ?= v0.7.0
LOCALBIN := $(CURDIR)/bin
KUBECONFORM := $(LOCALBIN)/kubeconform

export PATH := $(LOCALBIN):$(PATH)

# ---------------------------------------------------------------------------
# Help
# ---------------------------------------------------------------------------

.PHONY: help
help: ## Show available targets
	@echo "k8s-dencer  (provider=$(CLUSTER_PROVIDER)  tag=$(IMAGE_TAG))"
	@echo
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------

$(LOCALBIN):
	@mkdir -p $(LOCALBIN)

$(KUBECONFORM): | $(LOCALBIN)
	@echo "==> installing kubeconform $(KUBECONFORM_VERSION)"
	@GOBIN=$(LOCALBIN) go install github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION)

.PHONY: tools
tools: $(KUBECONFORM) ## Install pinned tooling into ./bin

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

.PHONY: build
build: ## Compile the Go binaries locally
	go build ./...

.PHONY: test
test: ## Run Go and UI tests
	go vet ./...
	go test ./...
	cd ui && npm run typecheck

.PHONY: images
images: ## Build native-arch images for the local cluster
	@echo "==> building images tagged $(IMAGE_TAG) (native arch)"
	docker buildx build --load \
		-f build/go.Dockerfile \
		--build-arg COMPONENT=planner \
		--build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE_PREFIX)-planner:$(IMAGE_TAG) .
	docker buildx build --load \
		-f build/go.Dockerfile \
		--build-arg COMPONENT=ui-backend \
		--build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE_PREFIX)-ui-backend:$(IMAGE_TAG) .
	docker buildx build --load \
		-f build/go.Dockerfile \
		--build-arg COMPONENT=executor \
		--build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE_PREFIX)-executor:$(IMAGE_TAG) .
	docker buildx build --load \
		-f build/ui.Dockerfile \
		-t $(IMAGE_PREFIX)-ui-frontend:$(IMAGE_TAG) .

.PHONY: images-release
images-release: ## Build multi-arch images (buildx cannot --load these; use --push)
	@echo "==> building $(PLATFORMS) images tagged $(IMAGE_TAG)"
	@echo "    note: multi-platform builds cannot be loaded into the local"
	@echo "    docker image store; add --push and a registry to publish."
	docker buildx build --platform $(PLATFORMS) \
		-f build/go.Dockerfile \
		--build-arg COMPONENT=planner \
		--build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE_PREFIX)-planner:$(IMAGE_TAG) .
	docker buildx build --platform $(PLATFORMS) \
		-f build/go.Dockerfile \
		--build-arg COMPONENT=ui-backend \
		--build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE_PREFIX)-ui-backend:$(IMAGE_TAG) .
	docker buildx build --platform $(PLATFORMS) \
		-f build/go.Dockerfile \
		--build-arg COMPONENT=executor \
		--build-arg VERSION=$(IMAGE_TAG) \
		-t $(IMAGE_PREFIX)-executor:$(IMAGE_TAG) .
	docker buildx build --platform $(PLATFORMS) \
		-f build/ui.Dockerfile \
		-t $(IMAGE_PREFIX)-ui-frontend:$(IMAGE_TAG) .

.PHONY: images-load
images-load: ## Make locally built images visible to the cluster
ifeq ($(CLUSTER_PROVIDER),orbstack)
	@echo "==> orbstack shares the docker image store; nothing to load"
else ifeq ($(CLUSTER_PROVIDER),k3d)
	k3d image import $(IMAGE_PREFIX)-planner:$(IMAGE_TAG) $(IMAGE_PREFIX)-ui-backend:$(IMAGE_TAG) $(IMAGE_PREFIX)-executor:$(IMAGE_TAG) $(IMAGE_PREFIX)-ui-frontend:$(IMAGE_TAG)
else ifeq ($(CLUSTER_PROVIDER),kind)
	kind load docker-image $(IMAGE_PREFIX)-planner:$(IMAGE_TAG) $(IMAGE_PREFIX)-ui-backend:$(IMAGE_TAG) $(IMAGE_PREFIX)-executor:$(IMAGE_TAG) $(IMAGE_PREFIX)-ui-frontend:$(IMAGE_TAG)
else ifeq ($(CLUSTER_PROVIDER),minikube)
	minikube image load $(IMAGE_PREFIX)-planner:$(IMAGE_TAG)
	minikube image load $(IMAGE_PREFIX)-ui-backend:$(IMAGE_TAG)
	minikube image load $(IMAGE_PREFIX)-executor:$(IMAGE_TAG)
	minikube image load $(IMAGE_PREFIX)-ui-frontend:$(IMAGE_TAG)
else
	$(error unknown CLUSTER_PROVIDER: $(CLUSTER_PROVIDER))
endif

# ---------------------------------------------------------------------------
# Lint
# ---------------------------------------------------------------------------

.PHONY: lint
lint: $(KUBECONFORM) ## Chart portability gate: lint, render and assert the contract
	@KUBECONFORM=$(KUBECONFORM) CHART=$(CHART) ./hack/lint-chart.sh

# ---------------------------------------------------------------------------
# Deploy
# ---------------------------------------------------------------------------

.PHONY: deploy
deploy: ## Install/upgrade the product chart with the provider overlay
	helm upgrade --install $(RELEASE) $(CHART) \
		--namespace $(NAMESPACE) --create-namespace \
		-f $(CHART)/ci/$(CLUSTER_PROVIDER)-values.yaml \
		--set planner.image.tag=$(IMAGE_TAG) \
		--set uiBackend.image.tag=$(IMAGE_TAG) \
		--set executor.image.tag=$(IMAGE_TAG) \
		--set uiFrontend.image.tag=$(IMAGE_TAG) \
		--wait --timeout 5m

.PHONY: ui
ui: ## Port-forward the UI and print its URL (Ctrl-C to stop)
	@if lsof -nP -iTCP:$(UI_PORT) -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "port $(UI_PORT) is already in use by:"; \
		lsof -nP -iTCP:$(UI_PORT) -sTCP:LISTEN | tail -n +2 | awk '{print "    " $$1 " (pid " $$2 ")"}'; \
		echo "retry on another port:  make ui UI_PORT=8091"; \
		exit 1; \
	fi
	@svc=$$(kubectl get svc -n $(NAMESPACE) -l app.kubernetes.io/component=ui-frontend \
		-o jsonpath='{.items[0].metadata.name}' 2>/dev/null); \
	if [ -z "$$svc" ]; then \
		echo "no ui-frontend service in namespace $(NAMESPACE); run 'make deploy' first"; \
		exit 1; \
	fi; \
	echo "==> k8s-dencer UI"; \
	echo "    http://localhost:$(UI_PORT)"; \
	lb=$$(kubectl get svc -n $(NAMESPACE) $$svc \
		-o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null); \
	if [ -n "$$lb" ]; then \
		echo "    http://$$lb                     (LoadBalancer)"; \
		echo "    http://$$svc.$(NAMESPACE).k8s.orb.local  (OrbStack DNS)"; \
	fi; \
	echo "    Ctrl-C to stop"; \
	kubectl port-forward -n $(NAMESPACE) svc/$$svc $(UI_PORT):80

.PHONY: capture-fixture
capture-fixture: ## Capture the live snapshot into test/fixtures/$(S).yaml
	@if [ -z "$(S)" ]; then echo "usage: make capture-fixture S=<scenario>"; exit 1; fi
	@mkdir -p test/fixtures
	@kubectl port-forward -n $(NAMESPACE) deploy/$(RELEASE)-planner $(FIXTURE_PORT):8081 >/dev/null 2>&1 & \
	pf=$$!; \
	trap "kill $$pf 2>/dev/null" EXIT; \
	sleep 4; \
	curl -sf -m 15 http://localhost:$(FIXTURE_PORT)/debug/snapshot -o test/fixtures/$(S).yaml \
		&& echo "captured test/fixtures/$(S).yaml ($$(wc -c < test/fixtures/$(S).yaml | tr -d ' ') bytes)" \
		|| { echo "capture failed; is the planner running and its cache synced?"; exit 1; }

.PHONY: status
status: ## Show what is running
	kubectl get pods,svc,pvc -n $(NAMESPACE)

.PHONY: logs
logs: ## Tail the planner log
	kubectl logs -n $(NAMESPACE) -l app.kubernetes.io/component=planner -f --tail=50

# Tier 3 of the auth story: a plain bearer token, for the POC and for CI. The
# production path is OIDC single sign-on (auth.oidc.*), where the API server
# validates an ID token from an issuer it already trusts.
TOKEN_SA       ?= dencer-operator
TOKEN_DURATION ?= 8h

.PHONY: token
token: ## Mint an operator token for the UI (paste it when prompted)
	@kubectl get serviceaccount $(TOKEN_SA) -n $(NAMESPACE) >/dev/null 2>&1 \
		|| kubectl create serviceaccount $(TOKEN_SA) -n $(NAMESPACE) >/dev/null
	@# Granting the operator role, not the viewer role, so the same token keeps
	@# working when the executor lands in M10 and the UI gains a run button.
	@kubectl get rolebinding $(TOKEN_SA) -n $(NAMESPACE) >/dev/null 2>&1 \
		|| kubectl create rolebinding $(TOKEN_SA) -n $(NAMESPACE) \
			--clusterrole=$(RELEASE)-consolidation-operator \
			--serviceaccount=$(NAMESPACE):$(TOKEN_SA) >/dev/null
	@echo "# ServiceAccount $(NAMESPACE)/$(TOKEN_SA), valid $(TOKEN_DURATION)." >&2
	@echo "# Paste this into the UI's sign-in field." >&2
	@kubectl create token $(TOKEN_SA) -n $(NAMESPACE) --duration=$(TOKEN_DURATION)

.PHONY: undeploy
undeploy: ## Remove the product release
	-helm uninstall $(RELEASE) --namespace $(NAMESPACE)

# ---------------------------------------------------------------------------
# Demo fabric (POC only — never part of the product chart)
# ---------------------------------------------------------------------------

.PHONY: kwok-up
kwok-up: ## Install the KWOK fake-node fabric
	@# Charts are served from the classic repo; the OCI path advertised in the
	@# KWOK docs currently has no tags published.
	helm repo add kwok https://kwok.sigs.k8s.io/charts/ >/dev/null 2>&1 || true
	helm repo update kwok >/dev/null
	helm upgrade --install kwok kwok/kwok --version $(KWOK_CHART_VERSION) \
		--namespace $(KWOK_NAMESPACE) --create-namespace \
		-f demo/kwok-values.yaml --wait --timeout 3m
	helm upgrade --install kwok-stage-fast kwok/stage-fast --version $(KWOK_CHART_VERSION) \
		--namespace $(KWOK_NAMESPACE) --wait --timeout 2m

.PHONY: kwok-down
kwok-down: ## Remove the KWOK fabric
	-helm uninstall kwok-stage-fast --namespace $(KWOK_NAMESPACE)
	-helm uninstall kwok --namespace $(KWOK_NAMESPACE)

# No --wait on the demo chart. Deployment readiness is not a meaningful gate
# here: kwok-controller fabricates pod readiness with podPlayStageParallelism 4,
# and its pod-ready Stage only selects pods still in phase Pending. A pod that
# reaches Running without Ready=True never re-matches the stage and stays
# not-ready forever, so --wait turns a cosmetic fabric quirk into a failed
# install. Use `make demo-wait` to block on pods being *scheduled*, which is
# what the planner actually cares about.
.PHONY: demo-up
demo-up: ## Install the synthetic topology (SCENARIO=a-fragmented)
	helm upgrade --install $(DEMO_RELEASE) demo/charts/dencer-demo \
		--namespace $(DEMO_NAMESPACE) --create-namespace \
		--set scenario=$(SCENARIO)

.PHONY: demo-wait
demo-wait: ## Block until every demo pod is scheduled onto a node
	@echo "==> waiting for demo pods to be scheduled"
	@for i in $$(seq 1 60); do \
		unscheduled=$$(kubectl get pods -n $(DEMO_NAMESPACE) \
			--field-selector spec.nodeName= --no-headers 2>/dev/null | wc -l | tr -d ' '); \
		total=$$(kubectl get pods -n $(DEMO_NAMESPACE) --no-headers 2>/dev/null | wc -l | tr -d ' '); \
		if [ "$$unscheduled" = "0" ] && [ "$$total" != "0" ]; then \
			echo "    $$total pods scheduled"; exit 0; \
		fi; \
		sleep 2; \
	done; \
	echo "    timed out with $$unscheduled pod(s) unscheduled"; exit 1

# Deletes the namespace as well as the release. A failed `helm upgrade` can
# leave resources behind that are no longer in the release manifest, and
# `helm uninstall` will not touch those orphans — they then show up as
# workloads from a scenario that is supposedly not active.
.PHONY: demo-down
demo-down: ## Remove the synthetic topology (deletes the fake nodes)
	-helm uninstall $(DEMO_RELEASE) --namespace $(DEMO_NAMESPACE)
	-kubectl delete namespace $(DEMO_NAMESPACE) --wait=false
	-kubectl delete nodes -l dencer.io/synthetic=true --wait=false

.PHONY: fabric-reset
fabric-reset: ## Uncordon every KWOK node (undo an executor run)
	@# Draining a node makes the node controller add
	@# node.kubernetes.io/unschedulable to .spec.taints, owned by the
	@# cluster's own field manager. The demo chart also manages .spec.taints,
	@# so a server-side apply then fails with a field-manager conflict — which
	@# is how `make scenario` breaks after a run. A scenario switch is a fabric
	@# reset, so uncordoning first is both the fix and the honest semantics.
	@nodes=$$(kubectl get nodes -l type=kwok -o jsonpath='{range .items[?(@.spec.unschedulable)]}{.metadata.name}{" "}{end}'); \
	if [ -n "$$nodes" ]; then \
		echo "==> uncordoning $$(echo $$nodes | wc -w | tr -d ' ') drained node(s)"; \
		kubectl uncordon $$nodes >/dev/null; \
	else \
		echo "==> no cordoned KWOK nodes"; \
	fi

.PHONY: scenario
scenario: ## Switch scenario: make scenario S=b-pdb-blocked
	@if [ -z "$(S)" ]; then \
		echo "usage: make scenario S=<a-fragmented|b-pdb-blocked|c-topology-spread|d-anti-affinity|e-tainted-pool|f-stateful>"; \
		exit 1; \
	fi
	@$(MAKE) --no-print-directory fabric-reset
	helm upgrade --install $(DEMO_RELEASE) demo/charts/dencer-demo \
		--namespace $(DEMO_NAMESPACE) --create-namespace \
		--set scenario=$(S)
	@$(MAKE) --no-print-directory demo-wait

.PHONY: demo
demo: kwok-up demo-up images images-load deploy ## Full POC: fabric + topology + product
	@echo
	@echo "==> demo ready. 'make ui' to open it, 'make down' to tear it all down."

.PHONY: down
down: undeploy demo-down kwok-down ## Remove every release this repo installs

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(LOCALBIN) ui/dist

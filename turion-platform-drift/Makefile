GO      ?= go
BIN     ?= bin/turion-platform-drift
SRC     ?= ./ground-service
OUT     ?= ./reports
OUT_CP  ?= ./reports-cp
TARGET  ?= kubernetes
HELM    ?= helm
KUBECTL ?= kubectl

# The kwok cluster gives a real kube-apiserver with no container runtime.
KWOK_CLUSTER ?= ground
export KUBECONFIG ?= $(HOME)/.kwok/clusters/$(KWOK_CLUSTER)/kubeconfig.yaml
export PATH := $(HOME)/.local/bin:/usr/local/go/bin:/usr/local/bin:$(PATH)

.PHONY: all build test fmt vet lint scan generate validate ci clean demo rules help \
        verify verify-fast cluster cluster-down crds fuzz stress race bench cover

all: ci

build: ## compile the CLI
	$(GO) build -trimpath -o $(BIN) .

test: ## run the full test suite
	$(GO) test -count=1 ./...

race: ## full suite under the race detector
	$(GO) test -race -count=1 ./...

stress: ## 3000-file repository, adversarial inputs, determinism, idempotency
	$(GO) test -count=1 -run 'TestStress|TestAdversarial|TestScanIsDeterministic|TestGenerateIsIdempotent' .

fuzz: ## fuzz every parser (bounded; CI runs longer)
	$(GO) test -run XXX -fuzz FuzzParse -fuzztime 30s ./internal/yamlite/
	$(GO) test -run XXX -fuzz FuzzScanText -fuzztime 20s .
	$(GO) test -run XXX -fuzz FuzzScanGoSource -fuzztime 15s .

bench: ## benchmarks
	$(GO) test -run XXX -bench . -benchmem

cover: ## coverage summary
	$(GO) test -count=1 -cover ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

lint: fmt-check vet

scan: ## DoD: portability scorecard
	$(GO) run turion-platform-drift scan $(SRC)

generate: ## both targets: operator-free and crossplane
	$(GO) run turion-platform-drift scan $(SRC) --generate --out $(OUT) --target kubernetes --fail-on-residual-aws
	$(GO) run turion-platform-drift scan $(SRC) --generate --out $(OUT_CP) --target crossplane --fail-on-residual-aws

validate: ## air-gapped validation; renders the Helm chart when helm is present
	$(GO) run turion-platform-drift validate $(OUT)
	$(GO) run turion-platform-drift validate $(OUT_CP)
	$(GO) run turion-platform-drift validate ./templates

cluster: ## start a real kube-apiserver (etcd + apiserver, no Docker)
	bash scripts/kwok-cluster.sh up $(KWOK_CLUSTER)

cluster-down:
	bash scripts/kwok-cluster.sh down $(KWOK_CLUSTER)

crds: ## install upstream CRDs at current versions
	bash scripts/install-crds.sh

verify: ## the whole Definition of Done, executed (31 gates)
	scripts/verify.sh

verify-fast: ## everything that needs no API server
	scripts/verify.sh --no-cluster

ci: lint build test generate validate ## non-cluster CI loop

demo: ## scorecard + generation + real kubectl dry-run when a cluster exists
	$(GO) run turion-platform-drift scan $(SRC) --generate --out $(OUT) --target crossplane --fail-on-residual-aws --server-dry-run
	$(GO) run turion-platform-drift validate $(OUT_CP) --server-dry-run

clean:
	rm -rf bin reports reports-cp

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

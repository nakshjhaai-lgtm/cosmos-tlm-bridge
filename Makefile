# cosmos-tlm-bridge — developer entry points
SHELL := /bin/bash
PY    := python3
SRC   := src
PORT  ?= 2900
OUT   ?= artifacts
GO    ?= go

.PHONY: help install test test-py test-go lint demo run smoke red reports serve bridge docker drift-scan drift-verify clean fmt

help: ## show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

install: ## create a uv virtualenv and install dev deps
	uv venv --python 3.11
	uv pip install -e ".[dev]"

fmt: ## format Python and Go
	ruff format $(SRC) tests
	cd go && $(GO) fmt ./...

lint: ## run ruff, mypy, and go vet
	ruff check $(SRC) tests
	mypy $(SRC)
	cd go && $(GO) vet ./...

test-py: ## run the Python test suite
	PYTHONPATH=$(SRC) $(PY) -m pytest tests/ -q

test-go: ## run the Go test suite
	cd go && $(GO) test ./...

test: test-py test-go ## run everything

demo: ## self-contained DROID pass acceptance run (target: < 45 s)
	PYTHONPATH=$(SRC) $(PY) -m cosmos_tlm_bridge.cli demo --out $(OUT)

smoke: ## fast smoke suite against a fresh in-process server
	PYTHONPATH=$(SRC) $(PY) -m cosmos_tlm_bridge.cli demo --suite droid_smoke.yaml --out $(OUT)

red: ## RED_HIGH thermal suite
	PYTHONPATH=$(SRC) $(PY) -m cosmos_tlm_bridge.cli demo --suite droid_thermal_red.yaml --out $(OUT)

run: ## run all three suites against an already-running COSMOS
	@for s in droid_smoke droid_thermal_red droid_pass_acceptance; do \
	  PYTHONPATH=$(SRC) $(PY) -m cosmos_tlm_bridge.cli run suites/$$s.yaml --out $(OUT) || exit 1; \
	done

serve: ## start the DROID_SIM OpenC3 reference server
	PYTHONPATH=$(SRC) $(PY) -m cosmos_tlm_bridge.cli serve --port $(PORT)

bridge: ## bridge typed frames to a webhook
	PYTHONPATH=$(SRC) $(PY) -m cosmos_tlm_bridge.cli bridge --duration 30 --dry-run

docker: ## one-command DROID pass: boots, accepts, reports, exits (< 45 s)
	docker compose up --build

drift-scan: ## cloud-agnostic ground-station portability scorecard (< 30 s)
	cd turion-platform-drift && $(GO) run turion-platform-drift scan ./ground-service

drift-verify: ## full 33-gate air-gapped Definition-of-Done harness
	cd turion-platform-drift && bash scripts/verify.sh

reports: ## regenerate JUnit + HTML from the last run and print paths
	@ls -1 $(OUT)

clean: ## remove build and report artifacts
	rm -rf $(OUT) .pytest_cache .ruff_cache .mypy_cache **/__pycache__ go/bin

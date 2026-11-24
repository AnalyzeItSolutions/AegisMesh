PYTHON ?= python3
VENV ?= .venv
PY = $(if $(wildcard $(VENV)/bin/python),$(VENV)/bin/python,$(PYTHON))

setdemo:
	bash scripts/demo.sh

up:
	$(PYTHON) -m venv $(VENV)
	$(VENV)/bin/pip install -q --upgrade pip
	$(VENV)/bin/pip install -q pytest grpcio grpcio-tools protobuf h3 ortools

up:
	docker compose up -d --wait

down:
	docker compose down -v

topics:
	bash scripts/create_topics.sh

test:
	cd services/sync-go && go test ./...
	cd services/dispatch-go && go test ./...
	cd services/routing-py && $(PY) -m pytest -q
	cd services/telemetry-go && go test ./...
	cd services/triage-py && $(PY) -m pytest -q

bench:
	@echo 'run the benchmark stages: python3 scripts/bench_assert.py --help'

.PHONY: demo setup up down topics test bench

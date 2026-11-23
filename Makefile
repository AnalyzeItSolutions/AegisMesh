up:
	docker compose up -d --wait

down:
	docker compose down -v

topics:
	bash scripts/create_topics.sh

test:
	cd services/sync-go && go test ./...
	cd services/dispatch-go && go test ./...
	cd services/routing-py && python -m pytest -q
	cd services/telemetry-go && go test ./...
	cd services/triage-py && python -m pytest -q

bench:
	@echo 'run the benchmark stages: python3 scripts/bench_assert.py --help'

.PHONY: up down topics test bench

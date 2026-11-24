# Contributing

Issues and pull requests are welcome.

1. `make setup`, then `make test` runs every service's tests. `bash scripts/acceptance.sh` is the full pass.
2. `make demo` starts the whole stack with a simulated fleet (see the README).
3. Contracts live in `proto/`; Go and Python stubs are generated (`scripts/gen_stubs.sh`), never hand-edited.
4. A change that touches tenant isolation, the gateway or the event log needs a test that tries to break it (see `scripts/gateway_check.py`).

Security problems: please open a private security advisory on GitHub instead of a public issue.

.DEFAULT_GOAL := help

.PHONY: \
	help \
	dev dev-server dev-web \
	build build-server build-web \
	test test-server test-web \
	lint lint-server lint-web \
	tidy tidy-server \
	clean clean-server clean-web

help:
	@printf "Available commands:\n"
	@printf "  make dev          Start server and web dev services together\n"
	@printf "  make dev-server   Start only the server dev service\n"
	@printf "  make dev-web      Start only the web dev service\n"
	@printf "  make build        Build server and web artifacts\n"
	@printf "  make build-server Build the Go server binary\n"
	@printf "  make build-web    Build the web app\n"
	@printf "  make test         Run server and web tests\n"
	@printf "  make test-server  Run Go tests\n"
	@printf "  make test-web     Run web tests\n"
	@printf "  make lint         Run server and web lint checks\n"
	@printf "  make lint-server  Check Go formatting and run go vet\n"
	@printf "  make lint-web     Run web ESLint and type checks\n"
	@printf "  make tidy         Tidy server dependencies\n"
	@printf "  make clean        Clean build artifacts\n"

dev:
	@./scripts/dev.sh all

dev-server:
	@./scripts/dev.sh server

dev-web:
	@./scripts/dev.sh web

build: build-server build-web

build-server:
	@$(MAKE) -C server build

build-web:
	@pnpm --dir web build

test: test-server test-web

test-server:
	@cd server && go test ./...

test-web:
	@pnpm --dir web test

lint: lint-server lint-web

lint-server:
	@cd server && files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		printf "These Go files need formatting:\n%s\n" "$$files"; \
		exit 1; \
	fi
	@cd server && go vet ./...

lint-web:
	@pnpm --dir web check

tidy: tidy-server

tidy-server:
	@$(MAKE) -C server tidy

clean: clean-server clean-web

clean-server:
	@$(MAKE) -C server clean

clean-web:
	@rm -rf web/dist

.DEFAULT_GOAL := help

.PHONY: \
	help \
	dev dev-server dev-web \
	build build-server build-web \
	test test-server test-server-race test-web test-release \
	lint lint-server lint-web \
	tidy tidy-server \
	api-types api-docs \
	clean clean-server clean-web \
	release

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
	@printf "  make test-server-race  Run Go tests with the race detector (opt-in)\n"
	@printf "  make test-release Run isolated release script checks\n"
	@printf "  make lint         Run server and web lint checks\n"
	@printf "  make lint-server  Check Go formatting and run go vet\n"
	@printf "  make lint-web     Run web ESLint and type checks\n"
	@printf "  make tidy         Tidy server dependencies\n"
	@printf "  make api-types    Regenerate Go and web types from api/openapi.yaml\n"
	@printf "  make api-docs     Regenerate skills/fast-ship API reference docs\n"
	@printf "  make clean        Clean build artifacts\n"
	@printf "  make release      Create a release commit, tag, and push\n"

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

# 竞态检测为可选目标，不进 test/test-server 必需门禁（INT-197 待评估）：
# INT-205 修复后 internal/service 单包 -race 实测 504-763s（2026-11-07 darwin/arm64
# 同机五连，不含其后合入的 Round 预算大用例），最差仍越过默认 10m，显式 -timeout
# 兑底；包级 TestMain 泄漏守卫保证结束时无 connectionOpener/worker/loop 残留。
# INT-200（2026-11-15）：套件增长后于 linux/amd64 12 核同机 4 次独立全包实测
# 1023~1097s（零 race、零超时，其中 Round 单测 ~160s、Bot 族 ~385s 为新旧共存的
# 正当成本），20m 余量仅 100~180s，经方向顾问审计按执行预算重新定标为 25m；
# 判定线：单包耗时 ≤ 25m 的 80%（20m）。不再随套件增长自动上调。
test-server-race:
	@cd server && go test -race -timeout 25m ./...

test-web:
	@pnpm --dir web test

test-release:
	@bash scripts/release-fixture.sh

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

api-types:
	@$(MAKE) -C server api-types
	@pnpm --dir web api-types

api-docs:
	@$(MAKE) -C server api-docs

clean: clean-server clean-web

clean-server:
	@$(MAKE) -C server clean

clean-web:
	@rm -rf web/dist

release:
	@./scripts/release.sh "$(VERSION)"

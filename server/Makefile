.PHONY: build run dev clean tidy api-types api-docs

# 构建
build:
	go build -o bin/fast_ship ./cmd/server

# 运行（生产模式）：config.yaml 交付的是占位密钥，启动校验会拒绝（INT-221）；
# 生产部署必须用 JWT_SECRET / ENCRYPTION_KEY 环境变量注入随机密钥。
run: build
	./bin/fast_ship

# 开发模式运行：默认本地配置交付的是占位密钥，这里给本地开发密钥回退
# （只用于本机 debug，不是生产凭据）。调用者显式给了 JWT_SECRET /
# ENCRYPTION_KEY，或用 CONFIG_PATH 指定了自带密钥的配置时，不注入——
# 覆盖部署者密钥会换钥，使既有登录态失效、库存密文无法解密。
dev:
	@if [ -n "$${CONFIG_PATH:-}" ]; then \
		go run ./cmd/server; \
	else \
		JWT_SECRET="$${JWT_SECRET:-local-dev-only-jwt-secret-0123456789}" \
		ENCRYPTION_KEY="$${ENCRYPTION_KEY:-0123456789abcdef0123456789abcdef}" \
		go run ./cmd/server; \
	fi

# 清理构建产物
clean:
	rm -rf bin/
	rm -rf data/

# 整理依赖
tidy:
	go mod tidy

# 从 api/openapi.yaml 重新生成 internal/api/types.gen.go
api-types:
	go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml

# 从 api/openapi.yaml 重新生成 skills/fast-ship/references/api.md
api-docs:
	go run ./cmd/apidocgen

# Docker 构建
docker-build:
	docker compose build

# Docker 运行
docker-up:
	docker compose up -d

# Docker 停止
docker-down:
	docker compose down

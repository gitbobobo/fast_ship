.PHONY: build run dev clean tidy api-types api-docs

# 构建
build:
	go build -o bin/fast_ship ./cmd/server

# 运行（生产模式）
run: build
	./bin/fast_ship

# 开发模式运行
dev:
	go run ./cmd/server

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

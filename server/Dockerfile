# 构建阶段
FROM golang:1.25-alpine AS builder
WORKDIR /build
RUN apk add --no-cache build-base
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o fast_ship ./cmd/server

# 运行阶段
FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
RUN mkdir -p /app/data
COPY --from=builder /build/fast_ship .
COPY --from=builder /build/configs ./configs
EXPOSE 4888
CMD ["./fast_ship"]

FROM node:22-bookworm-slim AS web-builder

WORKDIR /app/web

COPY VERSION /app/VERSION
COPY web/package.json web/pnpm-lock.yaml ./

RUN corepack enable && pnpm install --frozen-lockfile --ignore-scripts

COPY web/ .

RUN pnpm build

FROM golang:1.25-alpine AS server-builder

WORKDIR /app/server

RUN apk add --no-cache build-base

COPY server/go.mod server/go.sum ./

RUN go mod download

COPY server/ .

RUN CGO_ENABLED=1 GOOS=linux go build -o /out/fast_ship ./cmd/server

FROM alpine:3.19

WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata

COPY --from=server-builder /out/fast_ship /app/fast_ship
COPY --from=server-builder /app/server/configs /app/configs
COPY --from=web-builder /app/web/dist /app/web

ENV FAST_SHIP_WEB_DIST_DIR=/app/web

RUN mkdir -p /app/data

EXPOSE 4888

CMD ["./fast_ship"]

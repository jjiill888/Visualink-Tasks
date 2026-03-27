# syntax=docker/dockerfile:1
# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# --mount=type=cache 让 Go 编译缓存跨 build 复用
# 模板/CSS 改动不会触发重新编译 Go 代码
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o featuretrack .

# ── Run stage ─────────────────────────────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/featuretrack .
COPY templates/ templates/
COPY static/    static/

EXPOSE 8080
CMD ["./featuretrack"]

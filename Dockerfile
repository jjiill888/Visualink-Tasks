# syntax=docker/dockerfile:1
# ── Assets stage: 笔记编辑器（Milkdown）esbuild 打包 ─────────────────────────
FROM node:22-alpine AS assets

WORKDIR /app/web/notes-editor
COPY web/notes-editor/package.json web/notes-editor/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/notes-editor/src ./src
# build 脚本输出到 ../../static/，即 /app/static/notes-editor.{js,css}
RUN npm run build

# ── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# CGo 工具链：goheif 的 dav1d 子包用 C 实现 AV1-in-HEIF 解码
RUN apk add --no-cache gcc g++ musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .
# --mount=type=cache 让 Go 编译缓存跨 build 复用
# 模板/CSS 改动不会触发重新编译 Go 代码
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o featuretrack .

# ── Run stage ─────────────────────────────────────────────────────────────────
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata libstdc++ libgcc
WORKDIR /app

COPY --from=builder /app/featuretrack .
COPY templates/ templates/
COPY static/    static/
# 用容器内新鲜构建的编辑器产物覆盖仓库里提交的版本，保证与源码一致
COPY --from=assets /app/static/notes-editor.js /app/static/notes-editor.css static/

EXPOSE 8080
CMD ["./featuretrack"]

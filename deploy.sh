#!/bin/bash
set -e

echo "拉取最新代码..."
git pull origin main

# 云笔记协作（y-sweet）密钥：首次部署自动生成 .env（已 gitignore，不入库）。
# YSWEET_PUBLIC_PORT 是 y-sweet 对宿主机暴露的直连端口，从 8081 起自动跳过
# 已占用端口（生成后固定在 .env 里，想换端口直接改文件再 up -d）
if [ ! -f .env ]; then
  echo "生成 y-sweet 密钥对（首次部署）..."
  AUTH_JSON=$(sudo docker run --rm ghcr.io/jamsocket/y-sweet:latest y-sweet gen-auth --json)
  PORT=8081
  while ss -ltn 2>/dev/null | grep -q ":$PORT "; do PORT=$((PORT+1)); done
  {
    echo "YSWEET_PRIVATE_KEY=$(echo "$AUTH_JSON" | grep -oP '"private_key": "\K[^"]+')"
    echo "YSWEET_SERVER_TOKEN=$(echo "$AUTH_JSON" | grep -oP '"server_token": "\K[^"]+')"
    echo "YSWEET_PUBLIC_PORT=$PORT"
  } > .env
  echo "已写入 .env（直连端口 $PORT）"
fi

echo "重建并重启容器..."
sudo docker compose up -d --build

echo "清理旧镜像..."
sudo docker image prune -f

echo "部署完成"
sudo docker compose ps

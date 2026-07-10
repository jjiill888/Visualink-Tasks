#!/bin/bash
set -e

echo "拉取最新代码..."
git pull origin main

# 云笔记协作（y-sweet）密钥：首次部署自动生成 .env（已 gitignore，不入库）。
# YSWEET_PUBLIC_PORT 是 y-sweet 对宿主机暴露的直连端口。
# 端口占用要查两处：ss（宿主进程）+ docker 已发布端口（docker 层的分配
# ss 不一定看得见，报 "port is already allocated" 的就是这种）；
# ysweet 自己的容器不算冲突（本脚本管理它）
port_busy() {
  # ysweet 自己占着不算冲突（正常运行中的重部署，docker-proxy 会出现在 ss 里）
  sudo docker ps -a --format '{{.Names}} {{.Ports}}' 2>/dev/null \
    | grep ysweet | grep -q ":$1->" && return 1
  ss -ltn 2>/dev/null | grep -q ":$1 " && return 0
  sudo docker ps -a --format '{{.Names}} {{.Ports}}' 2>/dev/null \
    | grep -q ":$1->" && return 0
  return 1
}

if [ ! -f .env ]; then
  echo "生成 y-sweet 密钥对（首次部署）..."
  AUTH_JSON=$(sudo docker run --rm ghcr.io/jamsocket/y-sweet:latest y-sweet gen-auth --json)
  {
    echo "YSWEET_PRIVATE_KEY=$(echo "$AUTH_JSON" | grep -oP '"private_key": "\K[^"]+')"
    echo "YSWEET_SERVER_TOKEN=$(echo "$AUTH_JSON" | grep -oP '"server_token": "\K[^"]+')"
  } > .env
fi

# 每次部署都校验直连端口（.env 缺行则补，端口被占则自动换并改写 .env）
PORT=$(grep -oP '^YSWEET_PUBLIC_PORT=\K.*' .env 2>/dev/null || echo 8081)
if port_busy "$PORT"; then
  OLD=$PORT
  while port_busy "$PORT"; do PORT=$((PORT+1)); done
  echo "直连端口 $OLD 被占用，改用 $PORT"
fi
if grep -q '^YSWEET_PUBLIC_PORT=' .env; then
  sed -i "s/^YSWEET_PUBLIC_PORT=.*/YSWEET_PUBLIC_PORT=$PORT/" .env
else
  echo "YSWEET_PUBLIC_PORT=$PORT" >> .env
fi
echo "y-sweet 直连端口：$PORT"

echo "重建并重启容器..."
sudo docker compose up -d --build

echo "清理旧镜像..."
sudo docker image prune -f

echo "部署完成"
sudo docker compose ps

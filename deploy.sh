#!/bin/bash
set -e

echo "拉取最新代码..."
git pull origin main

echo "重建并重启容器..."
sudo docker compose up -d --build

echo "清理旧镜像..."
sudo docker image prune -f

echo "部署完成"
sudo docker compose ps

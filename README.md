# Visualink Tasks

Visualink!内部功能需求管理系统。

## 快速启动（Docker）

```bash
# 构建并启动（第一次约 1-2 分钟）
docker compose up -d --build

# 访问
open http://localhost:8080

# 查看日志
docker compose logs -f

# 停止
docker compose down
```

数据库文件保存在 `./data/app.db`，容器重建不会丢失。

## 本地开发（需要 Go 1.22+）

```bash
go mod tidy          # 下载依赖
go run .             # 启动，监听 :8080
```

## 功能说明

| 角色 | 权限 |
|------|------|
| 产品经理 (pm) | 提交功能、创建功能组、查看全部功能 |
| 开发工程师 (dev) | 查看全部功能、更新状态（待处理→进行中→完成） |

### 路由

| 路径 | 说明 |
|------|------|
| `/dashboard` | 主看板，含筛选 + 提交表单 |
| `/features/mine` | 我的提交 |
| `/groups` | 功能组列表 + 新建 |
| `/groups/{id}` | 功能组详情 |
| `PATCH /features/{id}/status` | HTMX 状态更新 |

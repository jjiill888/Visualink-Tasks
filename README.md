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

## 云笔记模块（notes）

所见即所得的 Markdown 笔记（Milkdown 编辑器，输入 `## ` 自动变标题），支持中文全文搜索（SQLite FTS5 trigram）、历史版本、图片/文件附件。

Markdown 兼容 CommonMark + GFM（表格对齐、任务列表、删除线、自动链接），另支持：数学公式 `$...$` / `$$...$$`（KaTeX 自托管渲染，双击编辑源码）、Emoji 短代码（`:smiley:` → 😃）、YAML front matter、代码块多语言语法高亮（highlight.js 自托管按需加载，29 种常用语言，```` ```go ````/```` ```py ```` 等标注即生效）；内联 HTML 标签原样保留显示（不渲染执行）。

表格：输入 `|3x2|` + 空格插入 3 列 2 行表格；光标进入表格时浮出加行/加列/删行/删列/对齐/删表工具条，Tab 在单元格间移动。

编辑页左侧侧栏（Typora 式）：鼠标移到窗口左缘临时探出，顶栏「侧栏」按钮点开常驻（再点关闭，刷新保持）；底部在「文件」（工作区文档列表，公共/私人分组、当前文档高亮）与「大纲」（当前文档目录，点击跳转、实时跟随内容更新）两视图间切换。顶栏另有配色按钮：自动（跟随系统）→ 浅色 → 深色循环切换。

### 实时协作（阶段二）

多人同时编辑同一篇笔记，互相实时看到对方的输入和光标（带用户名名牌）。基于 [y-sweet](https://github.com/jamsocket/y-sweet)（Yjs CRDT 服务器）：

- **部署**：`docker-compose.yml` 已含 `ysweet` 服务（数据存 `./data/ysweet/`，只在 compose 内网、不映射宿主端口）。首次部署前生成密钥对并写入 compose 同目录的 `.env`：
  ```bash
  docker run --rm ghcr.io/jamsocket/y-sweet:latest y-sweet gen-auth --json
  # 输出 private_key → .env 的 YSWEET_PRIVATE_KEY（给 y-sweet 的 --auth）
  # 输出 server_token → .env 的 YSWEET_SERVER_TOKEN（给 app 调管理 API）
  ```
- **鉴权链路**：浏览器不直连 y-sweet——先 `GET /notes/{id}/collab-token`（Go 校验笔记权限后代签房间 token），再连 `/collab/*` 反代 websocket（登录中间件 + y-sweet 校验 token 双层拦截）
- **持久化**：编辑静默 3 秒后前端把 Markdown 快照回写现有 `PUT /notes/{id}`（协作模式跳过乐观锁），FTS 搜索/历史版本机制不变；y-sweet 自身也落盘房间状态
- **降级**：不配 `Y_SWEET_URL` 环境变量 = 协作关闭，纯阶段一单人模式；配了但 y-sweet 挂掉时，编辑页 8 秒超时自动降级单人保存并提示「单人模式」
- **注意**：协作模式下「源码/预览」分屏不可用（分屏整体重写文档会覆盖他人输入）；标题修改也实时同步

### 权限规则

- 登录用户可查看/编辑所有**非私有**笔记（内网协作场景）
- `私有` 笔记仅创建者可见（编辑页右上角勾选）
- 删除仅创建者可操作（软删除）

### 路由

| 路径 | 说明 |
|------|------|
| `GET /notes` | 笔记列表（最近更新排序 + FTS 搜索框）；条目/新建均在新标签页打开 |
| `GET /notes/new` | 新建空笔记后 302 到编辑页 |
| `GET /notes/panel` | 编辑页左侧面板的文档列表片段（公共/私人分组） |
| `GET /notes/{id}` | WYSIWYG 编辑页（独立标签页、Typora 式极简风格；停顿 2 秒自动保存，乐观锁冲突返回 409） |
| `GET /notes/{id}/history` | 历史版本弹层（正文变化且距上次快照 >5 分钟才产生新版本，每篇最多 100 条） |
| `POST /notes/{id}/restore/{rid}` | 恢复到某版本（当前内容先自动存为一条版本） |
| `POST /notes/{id}/attachments` | 附件上传（≤20MB，扩展名白名单），存 `./data/attachments/{note_id}/` |
| `GET /attachments/{note_id}/{name}` | 附件下载（需登录，私有笔记附件外人不可见） |

### 前端构建（web/notes-editor）

编辑器岛用 esbuild 打包，产物为 `static/notes-editor.js`（已提交入库，改动源码后需重新构建）；构建同时把 KaTeX 的 CSS/字体从 node_modules 复制到 `static/vendor/katex/`（自托管，内网可用）。编辑页样式是手写的 `static/css/notes-editor.css`，不经打包：

```bash
cd web/notes-editor
npm install        # 首次
npm run build      # 输出到 ../../static/
```

- Docker 构建（`docker compose up -d --build`）内置 node 构建阶段，会自动重新打包，无需本地构建
- `Dockerfile.prebuilt` 快速部署路径直接 COPY 仓库里的 `static/`，改过编辑器源码时记得先在开发机 `npm run build` 并提交产物
- 服务启动时会对 `notes-editor.js` 计算内容 hash 做缓存版本号（同 `bundle.js` 机制），改版后无需手动清缓存

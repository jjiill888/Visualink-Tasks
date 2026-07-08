# Changelog

本文件作为功能跟踪文档，按阶段记录重要改动（新→旧）。

## 2026-07-08 云笔记模块 · 阶段一（基础功能，不含实时协作）

新增内部代号 `notes` 的云笔记模块：

- **数据库**（沿用同一 SQLite，启动时幂等建表）
  - `notes`（软删除、`is_private` 私有标记、毫秒精度 `updated_at` 兼作乐观锁 token、`updated_by` 记录最后更新人）
  - `note_revisions` 历史版本：正文有变化且距上一条超过 5 分钟才新增，每篇保留 100 条
  - `note_attachments` 附件元数据
  - `notes_fts`：FTS5 `tokenize='trigram'` 中文全文索引（external content + trigger 同步；启动时探测 trigram 支持，实测 modernc.org/sqlite v1.30.0 = SQLite 3.46.0 支持；搜索词不足 3 字符时回退 LIKE）
- **路由**：`/notes` 列表/搜索（HTMX）、编辑、PUT 自动保存（409 乐观锁）、软删除、历史版本查看/恢复、附件上传（≤20MB 白名单）与鉴权下载。全部挂在现有 `RequireAuth` 之后，完全复用现有账号体系
- **编辑器岛**：`web/notes-editor/`，Milkdown（@milkdown/kit）WYSIWYG，esbuild 打包为 `static/notes-editor.{js,css}`（产物入库）；停顿 2 秒自动保存、IME 组字保护、图片粘贴/拖拽上传、保存状态提示（Alpine）
- **部署**：Dockerfile 新增 node:22-alpine 构建阶段；`main.go` 复用 assets.Bundle 为编辑器 JS 提供内容 hash 版本号 + 预压缩
- 导航栏新增「笔记」入口
- `main.go` 支持 `PORT` 环境变量覆盖监听端口（默认 8080 不变，本地开发/测试用）

### 维护性说明

- 误提交的空文件 `*.db` 已从 git 索引移除；`.gitignore` 补充 `*.db`、`node_modules/`
- 仓库根目录的 `featuretrack` 二进制是 `Dockerfile.prebuilt` 部署流的产物，目前保留入库；**建议后续改为 CI 构建产物，不入库**

# Changelog

本文件作为功能跟踪文档，按阶段记录重要改动（新→旧）。

## 2026-07-08 云笔记 · 侧栏 Typora 化 + 手动配色切换

按用户反馈把侧栏从「多分区堆叠」改成 Typora 式极简，并新增浅色/深色手动切换：

- **侧栏单视图**：同一时间只显示「文件」或「大纲」一个视图，底部一条切换栏（文件 | 大纲 | ＋新建），选择记 localStorage；去掉「工作区」标题行与「固定」按钮
- **常驻逻辑并入「侧栏」按钮**：点开即常驻（刷新保持）、再点关闭；左缘悬停仍是临时探出，移出即收
- **配色切换**：顶栏新增主题按钮，循环 自动（跟随系统，实时响应系统切换）→ 浅色 → 深色，记 localStorage；深色样式从 `@media (prefers-color-scheme: dark)` 重构为 `<html>.ne-dark` 类驱动（避免整段样式维护两份），head 内联预判脚本防首屏闪烁；`color-scheme` 同步覆盖使表单控件/滚动条跟随
- 面板宽度 264→240px，常驻时不带浮层阴影（视觉上成为页面的一部分）

## 2026-07-08 云笔记 · 编辑页左侧面板（工作区 + 目录）

编辑页新增左侧抽屉面板，鼠标移到窗口左缘停留（180ms 防误触）或点顶栏「侧栏」按钮弹出：

- **工作区**：「公共文档」（全员的非私有笔记）与「私人文档」（自己的私有笔记）两组列表，当前文档高亮，「＋ 新建」直达 `/notes/new`。数据来自新端点 `GET /notes/panel`（渲染 `notes_panel_partial.html` 片段，打开时拉取、30 秒内不重复请求）
- **目录**：当前文档的 h1–h6 标题按层级缩进展示，点击平滑滚动到对应标题（`scroll-margin-top` 让开悬浮顶栏）；前端用 MutationObserver（防抖 300ms）直接从编辑器 DOM 生成，内容编辑时实时跟随，无需改动编辑器 bundle
- **交互**：可「固定」常驻（localStorage 记忆，刷新后保持展开）；未固定时鼠标移出 300ms 收起，Esc 立即收起；覆盖式抽屉不挤压正文
- 面板顶部让开 40px 悬浮顶栏（顶栏背景不透明且 z-index 更高）；深色模式适配

## 2026-07-08 云笔记 · 跨境性能优化（美国服务器 → 中国用户，高 RTT 场景）

保持 HTMX 服务端渲染哲学，不引入 SPA；优化目标是砍关键路径往返、砍首访字节、暖缓存：

- **编辑页秒出正文**：Markdown 原文由内联脚本立即以纯文本展示（`.ne-placeholder`，排版对齐编辑器正文），258KB 的编辑器 JS 在慢链路上加载期间页面不再白屏；挂载完成后移除占位，挂载失败保留原文只加红色提示（内容永远可读）
- **关键路径只留一个阻塞 CSS**：编辑页仅 `notes-editor.css`（18KB）阻塞渲染；`style.min.css`（42KB，仅弹层/按钮用）与 KaTeX CSS（24KB）用 `media="print" onload` 技巧异步加载，历史版本页同样处理
- **列表页空闲预取**：`/notes` 头部 `<link rel="prefetch">` 编辑器 JS/CSS/KaTeX CSS——浏览列表时浏览器空闲预取（immutable 缓存），点开笔记时编辑器资产已在本地，冷启动变热启动
- **编辑器 JS 减包 1089→471KB（br 259→127KB）**：两处重依赖换成自写轻量实现——① emoji 短代码弃用 remark-emoji（其依赖的 emojilib 完整名称库打包后 213KB），自写 `emoji-lite.js` 常用映射表（~2KB）等价替代，不在表里的短代码原文保留；② 表格编辑 UI 弃用官方 tableBlock 组件（内部 Vue 渲染，拖进约 113KB），自写 `table-toolbar.js` 原生 JS 工具条（见下节表格条目）。esbuild 顺带加 `__VUE_OPTIONS_API__=false` 等 define
- 现状确认无需改动：JS bundle 启动时 brotli 最高档预压缩 + immutable + ETag 304；动态响应 brotli q5 边压边发；系统字体栈零 webfont；KaTeX 字体按需加载
- **部署建议（未实施）**：加 TLS 反代启用 HTTP/2（消除 6 连接限制与队头阻塞），或将 /static/* 挂到国内 CDN 回源，对中国用户收益最大

## 2026-07-08 云笔记 · Markdown 兼容性完善（对照 Editor.md 全语法测试模板）

以 Editor.md 测试模板逐项对照补齐，全部改动在编辑器岛 `web/notes-editor/src/markdown-extra.js`：

- **数学公式（KaTeX）**：remark-math 解析 `$...$` / `$$...$$`，KaTeX 0.17 渲染；公式为原子节点，双击弹框编辑 LaTeX 源码；语法错误的公式红色原文显示不中断。KaTeX CSS/字体自托管在 `static/vendor/katex/`（`npm run build` 时从 node_modules 复制，内网可用，Dockerfile 已同步）。官方 @milkdown/plugin-math 停更于 7.5.x 与 kit 7.21 不兼容，故自实现轻量节点
- **Emoji 短代码**：`:smiley:` 转成 Unicode 字符（无外部图片依赖）；自写常用映射表 `emoji-lite.js` 实现（见上节减包条目）
- **YAML front matter**：remark-frontmatter + yaml 节点，修复文档顶部 `---...---` 被误解析成 setext 标题导致**保存时内容损坏**的问题；显示为低调可编辑的预格式块
- **修复**：内容图片的 `display:block` 样式误伤 ProseMirror 内部占位图（`.ProseMirror-separator`），行尾是公式等原子节点时段落被撑出大空隙
- 原样保留的内联 HTML 标签（`<s>`、`<sub>` 等，不渲染执行）加了灰阶代码样式与正文区分
- **表格编辑 UI**（自写 `table-toolbar.js`）：光标进入表格时上方浮出灰阶极简工具条——加行/加列/删行/删列/三向列对齐（序列化回 `:-:` 语法）/删表，命令直接调 prosemirror-tables 与 GFM 预设；`|3x2|` + 空格快速插入 3 列 2 行表格，Tab/Shift-Tab 在单元格间移动；粘贴含表格的 Markdown 正常解析
- **关闭浏览器拼写检查**：contenteditable 默认开启 spellcheck，英文单词满屏红色波浪线；编辑区与标题输入框均已关闭
- 调试钩子 `window.__notesEditorMarkdown()` 读取当前序列化结果（阶段二快照回写也会用到）

**兼容性验证**（无头 Chromium 全模板往返测试）：解析→序列化达到不动点（第二轮起逐字节稳定）；表格对齐（`:-:`/`--:`）、嵌套任务列表、缩进代码块、setext 标题、参考式链接、HTML 实体、转义、硬换行全部内容无损。已知限制：`[TOC]`、```` ```flow ````/```` ```seq ````（Editor.md 私有扩展）不渲染，按普通文本/代码块保留原文

## 2026-07-08 云笔记 · 体验优化（新标签页打开 + Typora 式编辑页）

不动数据层和路由逻辑的两项体验优化：

- **笔记在新标签页打开**：列表条目与「编辑」按钮改为普通 `<a target="_blank" rel="noopener">`；新建改为 `GET /notes/new`（登录中间件内，创建空笔记后 302 到 `/notes/{id}`），列表页「新建笔记」就是指向它的 `target="_blank"` 链接，原 `POST /notes` 移除。删除/搜索仍走 HTMX 留在列表页当前标签
- **编辑页 Typora 式极简重构**：`note_edit.html` 不再套 `base.html` 应用外壳（模板单独解析，新增 `renderStandalone` 助手）。40px 近乎隐形悬停显现的顶栏（返回/保存状态/历史版本/私有开关）、正文单栏居中 760px、无边框标题输入、16px/1.75 中文排版、灰阶代码块、左竖线引用、仅横线表格、细窄滚动条、`prefers-color-scheme: dark` 深色模式（#1e1e1e/#d4d4d4）
- **样式收敛**：编辑页样式全部收入手写的 `static/css/notes-editor.css`（内容 hash `?v=` 版本，同 bundle 做法），不进 style.css 的 us-* 体系；esbuild 不再产出 CSS（删除 `web/notes-editor/src/editor.css` 与旧 `static/notes-editor.css`，Dockerfile 相应调整），`note_revision.html` 改引新路径
- 保存状态指示简化为一个词（已保存/保存中…/保存失败），成功 2 秒后淡出，失败红色常驻；乐观锁冲突时红字可点击刷新页面

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

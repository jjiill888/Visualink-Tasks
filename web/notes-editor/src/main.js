// 云笔记编辑器岛（阶段一：单人编辑 + 自动保存 + 乐观锁）
//
// 页面契约（见 templates/note_edit.html / note_revision.html）：
//   #note-editor-root   编辑器挂载点，data-note-id / data-updated-at /
//                       data-save-url / data-upload-url / data-readonly
//   #note-initial       隐藏 textarea，携带初始 Markdown（天然 HTML 转义）
//   #note-title         标题输入框（只读页没有）
// 可见性/权限名单由页面自己的「权限」弹层（HTMX）管理，编辑器岛不参与
// 状态通过 window 事件 `notes-save-status` 通知页面上的 Alpine 组件：
//   detail.state ∈ idle | saving | saved | error | conflict
import { Editor, rootCtx, defaultValueCtx, editorViewOptionsCtx, editorViewCtx, remarkStringifyOptionsCtx } from '@milkdown/kit/core';
import { commonmark } from '@milkdown/kit/preset/commonmark';
import { gfm } from '@milkdown/kit/preset/gfm';
import { history } from '@milkdown/kit/plugin/history';
import { listener, listenerCtx } from '@milkdown/kit/plugin/listener';
import { clipboard } from '@milkdown/kit/plugin/clipboard';
import { upload, uploadConfig } from '@milkdown/kit/plugin/upload';
import { getMarkdown, replaceAll } from '@milkdown/kit/utils';

import { markdownExtra } from './markdown-extra.js';
import { inlineHtml } from './inline-html.js';
import { taskListItemView } from './task-list.js';
import { codeCopyView } from './code-copy.js';
import { tableToolbar } from './table-toolbar.js';
import { codeHighlight } from './code-highlight.js';
import { createScrollSync } from './scroll-sync.js';

// 样式不再走 esbuild：编辑页全部样式在手写的 static/css/notes-editor.css

function emitStatus(state) {
  window.dispatchEvent(new CustomEvent('notes-save-status', { detail: { state } }));
}

// 上传器：接 POST /notes/{id}/attachments，图片插入 image 节点，
// 其他文件插入下载链接文本。
function createUploader(uploadUrl) {
  return async (files, schema) => {
    const nodes = [];
    for (const file of Array.from(files)) {
      if (!file) continue;
      const fd = new FormData();
      fd.append('file', file, file.name);
      const resp = await fetch(uploadUrl, { method: 'POST', body: fd, credentials: 'same-origin' });
      if (!resp.ok) {
        throw new Error(await resp.text());
      }
      const data = await resp.json();
      if (data.is_image) {
        const node = schema.nodes.image.createAndFill({ src: data.url, alt: data.filename });
        if (node) nodes.push(node);
      } else if (schema.marks.link) {
        nodes.push(schema.text(data.filename, [schema.marks.link.create({ href: data.url })]));
      }
    }
    return nodes;
  };
}

// 自动保存：编辑停顿 2 秒触发 PUT；失败 5 秒后重试；
// 409（乐观锁冲突）后停止自动保存，提示用户刷新。
// IME 组字期间（compositionstart→compositionend）绝不发起保存，
// 避免中途序列化出半个拼音/注音的内容。
// 协作模式下 setLockFree(true)：base_updated_at 发空串跳过乐观锁——
// Yjs 才是并发事实来源，多客户端快照同一内容，409 反而是误伤。
// 锁基线仍持续跟踪服务器时间戳，降级回单人模式时无缝恢复乐观锁。
function createAutosave(opts) {
  let timer = null;
  let composing = false;
  let inflight = false;
  let pendingAfterFlight = false;
  let conflicted = false;
  let dirty = false;
  let lockFree = false;
  let baseUpdatedAt = opts.baseUpdatedAt;

  async function save() {
    if (conflicted) return;
    if (composing) { schedule(); return; }
    if (inflight) { pendingAfterFlight = true; return; }
    inflight = true;
    dirty = false;
    emitStatus('saving');
    try {
      const resp = await fetch(opts.saveUrl, {
        method: 'PUT',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...opts.payload(), base_updated_at: lockFree ? '' : baseUpdatedAt }),
      });
      if (resp.status === 409) {
        conflicted = true;
        emitStatus('conflict');
        return;
      }
      if (!resp.ok) throw new Error('save failed: ' + resp.status);
      const data = await resp.json();
      baseUpdatedAt = data.updated_at; // 服务器返回新时间戳，作为下次保存的锁基线
      emitStatus('saved');
    } catch (err) {
      dirty = true;
      emitStatus('error');
      timer = setTimeout(save, 5000); // 网络失败自动重试
    } finally {
      inflight = false;
      if (pendingAfterFlight && !conflicted) {
        pendingAfterFlight = false;
        schedule();
      }
    }
  }

  function schedule() {
    if (conflicted) return;
    dirty = true;
    clearTimeout(timer);
    timer = setTimeout(save, opts.delay || 2000);
  }

  // 页面卸载时的兜底冲刷：keepalive 让请求在页面关闭后仍能完成。
  // 仍携带锁基线——宁可这次兜底保存因冲突失败，也不静默覆盖他人的新版本。
  function flushOnUnload() {
    if (!dirty || conflicted || composing) return;
    fetch(opts.saveUrl, {
      method: 'PUT',
      credentials: 'same-origin',
      keepalive: true,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...opts.payload(), base_updated_at: lockFree ? '' : baseUpdatedAt }),
    }).catch(() => {});
  }

  return {
    markDirty: schedule,
    saveNow: () => { clearTimeout(timer); save(); },
    setComposing: (v) => {
      composing = v;
      if (!v) schedule(); // 组字结束后再进入保存倒计时
    },
    setLockFree: (v) => { lockFree = v; },
    flushOnUnload,
  };
}

async function mountEditor(root) {
  const readonly = root.dataset.readonly === '1';
  // 协作模式（服务端配置了 y-sweet 时置 data-collab）：编辑器先按阶段一
  // 正常挂载（defaultValue 直出，跨境弱网不等 ws），协作在后台异步接通；
  // 8 秒接不通自动降级，页面始终可编辑
  const collabMode = !readonly && root.dataset.collab === '1';
  const initialEl = document.getElementById('note-initial');
  const initial = initialEl ? initialEl.value : '';
  const titleEl = document.getElementById('note-title');

  let currentMarkdown = initial;
  let autosave = null;
  // 「源码/预览」分屏模式：true 时源码 textarea 是唯一事实来源，
  // Milkdown 只作只读渲染预览（uiEditable=false）
  let sourceMode = false;
  let uiEditable = true;
  // 协作状态（由 collab.js 回调驱动）。协作接通后分屏语义反转：共享 Yjs
  // 文档才是事实来源，源码栏变成它的实时镜像——独自在线（peers=1）时可
  // 编辑（replaceAll 写回共享文档，没有踩踏对象），两人及以上转只读
  // （否则防抖整文覆盖会吞掉别人正在敲的内容）；peers=0 表示连上后掉线、
  // 人数未知，按「不止自己」处理
  let collabActive = false;
  let collabPeers = 1;
  const srcIsTruth = () => !collabActive || collabPeers === 1;
  let mirrorSource = null;      // 镜像刷新（分屏初始化段赋值）
  let applySrcEditable = null;  // 只读/可编辑切换（同上）
  let onCollabBound = null;     // 协作接通时对齐源码栏（同上）

  if (!readonly) {
    autosave = createAutosave({
      saveUrl: root.dataset.saveUrl,
      baseUpdatedAt: root.dataset.updatedAt,
      delay: collabMode ? 3000 : 2000, // 协作规格：编辑静默 3 秒后快照回写
      payload: () => ({
        title: titleEl ? titleEl.value : '',
        content_md: currentMarkdown,
      }),
    });
  }

  const editor = await Editor.make()
    .config((ctx) => {
      ctx.set(rootCtx, root);
      ctx.set(defaultValueCtx, initial);
      // 序列化保真：列表符号用 - 而非默认的 *；
      // 修 Milkdown 的 spread 串型 bug —— list/listItem 的 spread 被解析层
      // 存成字符串 "false"（真值），导致紧凑列表保存后每项之间多出空行，
      // 这里用 join 覆盖强制紧凑
      ctx.update(remarkStringifyOptionsCtx, (prev) => ({
        ...prev,
        bullet: '-',
        join: [
          ...(prev.join || []),
          (left, right, parent) => {
            if ((parent.type === 'list' || parent.type === 'listItem') && parent.spread === 'false') return 0;
            return undefined;
          },
        ],
      }));
      // 关闭浏览器拼写检查：contenteditable 默认开启，英文单词会满屏红色波浪线
      ctx.update(editorViewOptionsCtx, (prev) => ({
        ...prev,
        attributes: { spellcheck: 'false', autocorrect: 'off', autocapitalize: 'off' },
      }));
      if (readonly) {
        ctx.update(editorViewOptionsCtx, (prev) => ({ ...prev, editable: () => false }));
      } else {
        ctx.update(editorViewOptionsCtx, (prev) => ({ ...prev, editable: () => uiEditable }));
        ctx.get(listenerCtx).markdownUpdated((_ctx, markdown, prevMarkdown) => {
          if (markdown === prevMarkdown) return;
          if (sourceMode) {
            // 源码是事实来源（单人 / 协作独处）：预览由 textarea 灌入
            // （replaceAll），序列化结果不能反向覆盖 currentMarkdown——
            // 保存的必须是用户敲的源码原文
            if (srcIsTruth()) return;
            // 协作多人：源码栏是共享文档的只读镜像，远端改动实时刷进来
            if (mirrorSource) mirrorSource(markdown);
          }
          currentMarkdown = markdown;
          autosave.markDirty();
        });
        ctx.update(uploadConfig.key, (prev) => ({
          ...prev,
          uploader: createUploader(root.dataset.uploadUrl),
          enableHtmlFileUploader: false,
        }));
      }
    })
    .use(commonmark)
    .use(gfm)
    .use(tableToolbar)
    .use(codeHighlight)
    .use(markdownExtra)
    .use(inlineHtml)
    .use(taskListItemView)
    .use(codeCopyView)
    // 协作模式不装本地 history：y-prosemirror 的共享撤销栈（Mod-z 键位由
    // CollabService 注入，只撤自己的操作）与 prosemirror-history 互斥。
    // 协作代码在独立 chunk 里懒加载（见下方 import()），这里传空数组占位
    .use(collabMode ? [] : [history])
    .use(listener)
    .use(clipboard)
    .use(upload)
    .create();

  const srcEl = document.getElementById('note-source');
  const modeBtn = document.getElementById('ne-mode-toggle');

  // ── 实时协作（阶段二）────────────────────────────────────────
  // 协作代码（yjs + y-prosemirror + y-sweet client，约 130KB）在独立 chunk
  // 懒加载：编辑器先挂载可用，协作后台接通——主 bundle 回到协作前体积。
  // 「源码/预览」在协作下照常可用：源码栏是共享文档的实时镜像，独自在线
  // 可编辑、多人在线只读（详见挂载函数顶部 collabActive 一节的说明）。
  if (collabMode) {
    const statusEl = document.getElementById('ne-collab-status');
    // 服务端预签的房间 token 随页直出（省一次 round-trip）；缺失时前端回退拉取
    let initialToken = null;
    try { initialToken = JSON.parse(root.dataset.collabToken || 'null'); } catch { /* 回退拉取 */ }
    import('./collab.js')
      .then(({ startCollab }) => startCollab({
        editor,
        noteId: root.dataset.noteId,
        username: root.dataset.username || '匿名',
        initialToken,
        currentMarkdown: () => currentMarkdown,
        titleEl,
        onReady: () => {
          // Yjs 接管并发，快照回写跳过乐观锁（多客户端存同一内容，409 是误伤）
          autosave.setLockFree(true);
          collabActive = true;
          // 绑定可能把编辑器内容换成房间内容（房间非空时），分屏下源码栏
          // 必须跟着对齐，否则下一次源码输入会拿旧文覆盖房间新文
          if (onCollabBound) onCollabBound();
        },
        onDegrade: () => {
          // 从未连上：单人保存模式（乐观锁本来就没关过），源码栏恢复单人语义
          collabActive = false;
          collabPeers = 1;
          if (applySrcEditable) applySrcEditable();
        },
        onPeers: (n) => {
          collabPeers = n;
          if (applySrcEditable) applySrcEditable();
        },
        onDocUpdate: () => {
          // 只读镜像时把改动（主要是远端的）刷进源码栏。listener 插件对
          // 远端事务不触发（addToHistory:false 被跳过），信号从 Yjs 层取
          if (!sourceMode || srcIsTruth() || !mirrorSource) return;
          const md = editor.action(getMarkdown());
          mirrorSource(md);
          currentMarkdown = md;
        },
        onStatus: (text, level) => {
          if (!statusEl) return;
          statusEl.textContent = text;
          statusEl.className = 'ne-collab-status' + (level ? ' ne-collab-' + level : '');
        },
      }))
      .catch(() => {
        // chunk 加载失败（弱网/缓存异常）：等同降级，单人模式继续可用
        collabActive = false;
      });
  }

  if (!readonly) {
    // IME 组字保护：编辑区、标题框、源码 textarea 都挂 composition 监听
    for (const el of [root, titleEl, srcEl].filter(Boolean)) {
      el.addEventListener('compositionstart', () => autosave.setComposing(true));
      el.addEventListener('compositionend', () => autosave.setComposing(false));
    }
    if (titleEl) titleEl.addEventListener('input', () => autosave.markDirty());
    // 离开页面前把未保存内容冲出去
    window.addEventListener('beforeunload', () => autosave.flushOnUnload());
  }

  // ── 「源码/预览」分屏模式 ─────────────────────────────────────
  // 左栏 textarea 编辑 Markdown 源码，右栏现有 Milkdown 实例转只读渲染预览；
  // 输入防抖 400ms 后 replaceAll 灌入预览。保存内容始终是源码原文。
  if (!readonly && srcEl && modeBtn) {
    let previewTimer = null;
    let previewPending = false; // 源码已改、预览还没灌入（防抖期内）
    let caretTimer = null;
    const srcLockEl = document.getElementById('ne-src-lock');

    // 滚动/光标同步（块锚点 + 分段线性插值，见 scroll-sync.js）
    const scrollSync = createScrollSync({
      srcEl,
      previewEl: root, // #note-editor-root 在分屏下是预览侧滚动容器
      getPmRoot: () => root.querySelector('.ProseMirror'),
    });

    // 协作多人时远端改动实时刷进源码栏。正在选取源码（复制中）就跳过
    // 这一拍，别把选区冲掉——下一次远端改动会带来完整最新内容
    mirrorSource = (markdown) => {
      if (document.activeElement === srcEl && srcEl.selectionStart !== srcEl.selectionEnd) return;
      srcEl.value = markdown;
    };

    // 独处可编辑 ↔ 多人只读镜像 的切换。转只读前先把防抖期内的源码改动
    // 冲进共享文档（刚加入的人必须拿到你敲的最后几个字），再以序列化结果
    // 重置镜像基线
    applySrcEditable = () => {
      if (!sourceMode) return;
      const canEdit = srcIsTruth();
      if (srcEl.readOnly === !canEdit) return;
      if (!canEdit) {
        clearTimeout(previewTimer);
        if (previewPending) {
          editor.action(replaceAll(srcEl.value));
          previewPending = false;
        }
      }
      // 两个方向都以序列化结果重置源码基线：转只读是镜像起点；恢复可编辑
      // 则消掉镜像防抖 200ms 的竞态——对方最后一击可能还没刷进 textarea
      srcEl.value = editor.action(getMarkdown());
      currentMarkdown = srcEl.value;
      srcEl.readOnly = !canEdit;
      if (srcLockEl) srcLockEl.hidden = canEdit;
    };

    // 协作接通：绑定可能已把编辑器内容换成房间内容（房间非空时采远端），
    // 分屏下把源码栏对齐到绑定后的真实内容，再按人数定只读
    onCollabBound = () => {
      if (!sourceMode) return;
      clearTimeout(previewTimer);
      previewPending = false;
      currentMarkdown = editor.action(getMarkdown());
      srcEl.value = currentMarkdown;
      applySrcEditable();
    };

    function setMode(split) {
      if (split === sourceMode) return;
      if (split) {
        // 进入分屏：以编辑器当前序列化结果为源码初值
        currentMarkdown = editor.action(getMarkdown());
        srcEl.value = currentMarkdown;
      } else {
        // 回到所见即所得：源码灌回编辑器（sourceMode 先置回，
        // 让 markdownUpdated 恢复接管 currentMarkdown）
        clearTimeout(previewTimer);
        if (!srcEl.readOnly) currentMarkdown = srcEl.value;
      }
      sourceMode = split;
      uiEditable = !split;
      // 只读镜像退出时不回灌：共享文档本来就是事实来源，replaceAll 反而
      // 会用镜像瞬间的旧文吞掉别人刚敲的内容
      if (!split && !srcEl.readOnly) editor.action(replaceAll(srcEl.value));
      // 空 setProps 触发 ProseMirror 重估 editable()，切换 contenteditable
      editor.action((ctx) => ctx.get(editorViewCtx).setProps({}));
      srcEl.hidden = !split;
      document.body.classList.toggle('ne-split', split);
      modeBtn.classList.toggle('ne-on', split);
      localStorage.setItem('ne-mode', split ? 'split' : 'wysiwyg');
      if (split) {
        applySrcEditable();
        if (!srcEl.readOnly) srcEl.focus();
        scrollSync.enable(); // 布局类已切换，此时测量的分栏宽度才是真实值
      } else {
        if (srcLockEl) srcLockEl.hidden = true;
        scrollSync.disable();
      }
    }

    srcEl.addEventListener('input', () => {
      if (!sourceMode) return;
      currentMarkdown = srcEl.value;
      autosave.markDirty();
      previewPending = true;
      clearTimeout(previewTimer);
      previewTimer = setTimeout(() => {
        if (!sourceMode) { previewPending = false; return; }
        editor.action(replaceAll(srcEl.value));
        previewPending = false;
        // 等一帧让 ProseMirror 完成布局，再重建锚点并把预览带到光标处
        requestAnimationFrame(() => scrollSync.previewChanged());
      }, 400);
    });

    // 光标移动（点击/方向键）也让预览跟随；输入期间预览还没更新（锚点是旧的），
    // 交给上面 previewChanged 处理，这里跳过
    document.addEventListener('selectionchange', () => {
      if (!sourceMode || document.activeElement !== srcEl) return;
      clearTimeout(caretTimer);
      caretTimer = setTimeout(() => {
        if (sourceMode && !previewPending) scrollSync.syncToCaret();
      }, 200);
    });

    modeBtn.addEventListener('click', () => setMode(!sourceMode));
    // 记住上次的模式选择。协作接通前先按单人语义进入，接通后 onCollabBound
    // 对齐源码栏内容并按人数定只读
    if (localStorage.getItem('ne-mode') === 'split') setMode(true);
  }

  // 调试/测试钩子：读取当前序列化后的 Markdown（阶段二快照回写也会用到）。
  // 分屏模式下以源码 textarea 为准——预览灌入有 400ms 防抖，可能滞后
  window.__notesEditorMarkdown = () =>
    sourceMode && srcEl ? srcEl.value : editor.action(getMarkdown());

  return editor;
}

document.addEventListener('DOMContentLoaded', () => {
  const root = document.getElementById('note-editor-root');
  if (!root) return;
  mountEditor(root)
    .then(() => {
      // 编辑器就绪，移除模板里的纯文本占位（跨境慢链路下的秒出内容）
      const ph = document.getElementById('note-placeholder');
      if (ph) ph.remove();
    })
    .catch((err) => {
      console.error('notes editor mount failed:', err);
      // 保留占位原文可读，只在顶部加提示，不清空内容
      const notice = document.createElement('div');
      notice.className = 'ne-load-error';
      notice.textContent = '编辑器加载失败，以下为只读原文，请刷新页面重试';
      root.prepend(notice);
    });
});

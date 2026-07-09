// 云笔记编辑器岛（阶段一：单人编辑 + 自动保存 + 乐观锁）
//
// 页面契约（见 templates/note_edit.html / note_revision.html）：
//   #note-editor-root   编辑器挂载点，data-note-id / data-updated-at /
//                       data-save-url / data-upload-url / data-readonly
//   #note-initial       隐藏 textarea，携带初始 Markdown（天然 HTML 转义）
//   #note-title         标题输入框（只读页没有）
//   #note-private       私有开关（仅 owner 可见，可选）
// 状态通过 window 事件 `notes-save-status` 通知页面上的 Alpine 组件：
//   detail.state ∈ idle | saving | saved | error | conflict
import { Editor, rootCtx, defaultValueCtx, editorViewOptionsCtx, editorViewCtx } from '@milkdown/kit/core';
import { commonmark } from '@milkdown/kit/preset/commonmark';
import { gfm } from '@milkdown/kit/preset/gfm';
import { history } from '@milkdown/kit/plugin/history';
import { listener, listenerCtx } from '@milkdown/kit/plugin/listener';
import { clipboard } from '@milkdown/kit/plugin/clipboard';
import { upload, uploadConfig } from '@milkdown/kit/plugin/upload';
import { getMarkdown, replaceAll } from '@milkdown/kit/utils';

import { markdownExtra } from './markdown-extra.js';
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
function createAutosave(opts) {
  let timer = null;
  let composing = false;
  let inflight = false;
  let pendingAfterFlight = false;
  let conflicted = false;
  let dirty = false;
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
        body: JSON.stringify({ ...opts.payload(), base_updated_at: baseUpdatedAt }),
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
    timer = setTimeout(save, 2000);
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
      body: JSON.stringify({ ...opts.payload(), base_updated_at: baseUpdatedAt }),
    }).catch(() => {});
  }

  return {
    markDirty: schedule,
    saveNow: () => { clearTimeout(timer); save(); },
    setComposing: (v) => {
      composing = v;
      if (!v) schedule(); // 组字结束后再进入保存倒计时
    },
    flushOnUnload,
  };
}

async function mountEditor(root) {
  const readonly = root.dataset.readonly === '1';
  const initialEl = document.getElementById('note-initial');
  const initial = initialEl ? initialEl.value : '';
  const titleEl = document.getElementById('note-title');
  // 可见性开关：顶栏文字按钮（仅 owner 有），显示当前状态，点击切换并立即保存
  const privateBtn = document.getElementById('ne-private-toggle');
  let isPrivate = privateBtn ? privateBtn.dataset.private === '1' : false;
  function renderPrivate() {
    privateBtn.textContent = isPrivate ? '私有' : '公开';
    privateBtn.title = isPrivate ? '仅自己可见，点击改为所有人可见' : '所有人可见，点击改为仅自己可见';
    privateBtn.classList.toggle('ne-on', isPrivate);
  }
  if (privateBtn) renderPrivate();

  let currentMarkdown = initial;
  let autosave = null;
  // 「源码/预览」分屏模式：true 时源码 textarea 是唯一事实来源，
  // Milkdown 只作只读渲染预览（uiEditable=false）
  let sourceMode = false;
  let uiEditable = true;

  if (!readonly) {
    autosave = createAutosave({
      saveUrl: root.dataset.saveUrl,
      baseUpdatedAt: root.dataset.updatedAt,
      payload: () => {
        const payload = {
          title: titleEl ? titleEl.value : '',
          content_md: currentMarkdown,
        };
        if (privateBtn) payload.is_private = isPrivate;
        return payload;
      },
    });
  }

  const editor = await Editor.make()
    .config((ctx) => {
      ctx.set(rootCtx, root);
      ctx.set(defaultValueCtx, initial);
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
          // 分屏模式下预览由 textarea 灌入（replaceAll），序列化结果不能
          // 反向覆盖 currentMarkdown——保存的必须是用户敲的源码原文
          if (sourceMode) return;
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
    .use(history)
    .use(listener)
    .use(clipboard)
    .use(upload)
    .create();

  const srcEl = document.getElementById('note-source');
  const modeBtn = document.getElementById('ne-mode-toggle');

  if (!readonly) {
    // IME 组字保护：编辑区、标题框、源码 textarea 都挂 composition 监听
    for (const el of [root, titleEl, srcEl].filter(Boolean)) {
      el.addEventListener('compositionstart', () => autosave.setComposing(true));
      el.addEventListener('compositionend', () => autosave.setComposing(false));
    }
    if (titleEl) titleEl.addEventListener('input', () => autosave.markDirty());
    if (privateBtn) {
      privateBtn.addEventListener('click', () => {
        isPrivate = !isPrivate;
        renderPrivate();
        autosave.saveNow();
      });
    }
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

    // 滚动/光标同步（块锚点 + 分段线性插值，见 scroll-sync.js）
    const scrollSync = createScrollSync({
      srcEl,
      previewEl: root, // #note-editor-root 在分屏下是预览侧滚动容器
      getPmRoot: () => root.querySelector('.ProseMirror'),
    });

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
        currentMarkdown = srcEl.value;
      }
      sourceMode = split;
      uiEditable = !split;
      if (!split) editor.action(replaceAll(srcEl.value));
      // 空 setProps 触发 ProseMirror 重估 editable()，切换 contenteditable
      editor.action((ctx) => ctx.get(editorViewCtx).setProps({}));
      srcEl.hidden = !split;
      document.body.classList.toggle('ne-split', split);
      modeBtn.classList.toggle('ne-on', split);
      localStorage.setItem('ne-mode', split ? 'split' : 'wysiwyg');
      if (split) {
        srcEl.focus();
        scrollSync.enable(); // 布局类已切换，此时测量的分栏宽度才是真实值
      } else {
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
    // 记住上次的模式选择
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

// 代码块多语言语法高亮 —— ProseMirror 装饰实现，不改文档内容（序列化零影响）。
// highlight.js 不进主 bundle：文档里出现带语言标注的代码块时才注入加载
// 自托管的 static/vendor/hljs/hljs.min.js（URL 由模板经 window.__notesHljs 提供，
// 同 KaTeX 的按需加载做法）。语言不认识或 hljs 未就绪时保持素颜纯文本。
// 着色：hljs 输出的 HTML 转成 Decoration.inline（span + .hljs-* 类），
// token 颜色在 notes-editor.css 里随深浅色板走。
import { $prose } from '@milkdown/kit/utils';
import { Plugin, PluginKey } from '@milkdown/kit/prose/state';
import { Decoration, DecorationSet } from '@milkdown/kit/prose/view';

const key = new PluginKey('notes-code-highlight');

let hljsLoading = null;

function loadHljs() {
  if (window.hljs) return Promise.resolve(window.hljs);
  if (!hljsLoading) {
    hljsLoading = new Promise((resolve, reject) => {
      const cfg = window.__notesHljs || {};
      const s = document.createElement('script');
      s.src = cfg.js || '/static/vendor/hljs/hljs.min.js';
      s.onload = () => resolve(window.hljs);
      s.onerror = () => reject(new Error('hljs load failed'));
      document.head.appendChild(s);
    });
  }
  return hljsLoading;
}

// hljs.highlight() 输出的 HTML 走一遍 DOM，把带类名的文本段落映射为
// 文档坐标上的 inline 装饰。嵌套 span 的类合并（如 hljs-title.function_）。
function decorateBlock(decos, node, pos) {
  const lang = (node.attrs.language || '').trim().toLowerCase();
  if (!lang || !window.hljs || !window.hljs.getLanguage(lang)) return;
  let html;
  try {
    html = window.hljs.highlight(node.textContent, { language: lang }).value;
  } catch {
    return; // 单个块解析失败不影响其他块
  }
  const tmp = document.createElement('template');
  tmp.innerHTML = html;
  let offset = pos + 1; // +1 进入 code_block 内容起点
  (function walk(el, classes) {
    for (const child of el.childNodes) {
      if (child.nodeType === Node.TEXT_NODE) {
        const len = child.nodeValue.length;
        if (classes) decos.push(Decoration.inline(offset, offset + len, { class: classes }));
        offset += len;
      } else if (child.nodeType === Node.ELEMENT_NODE) {
        const own = (child.getAttribute('class') || '').trim();
        walk(child, classes && own ? classes + ' ' + own : (own || classes));
      }
    }
  })(tmp.content, '');
}

function buildDecorations(doc) {
  const decos = [];
  doc.descendants((node, pos) => {
    if (node.type.name !== 'code_block') return;
    decorateBlock(decos, node, pos);
    return false; // 代码块无子块，不再下钻
  });
  return DecorationSet.create(doc, decos);
}

// 轻量扫描：文档里是否存在带语言标注的代码块（决定要不要加载 hljs）
function hasLangBlock(doc) {
  let found = false;
  doc.descendants((node) => {
    if (found) return false;
    if (node.type.name === 'code_block' && (node.attrs.language || '').trim()) found = true;
    return !found;
  });
  return found;
}

export const codeHighlight = $prose(() => {
  let loadKicked = false;
  return new Plugin({
    key,
    state: {
      init(_, { doc }) {
        return buildDecorations(doc);
      },
      apply(tr, old, _oldState, newState) {
        if (!tr.docChanged && !tr.getMeta(key)) return old.map(tr.mapping, tr.doc);
        return buildDecorations(newState.doc);
      },
    },
    view(editorView) {
      // 挂载后若存在带语言的代码块且 hljs 未就绪，加载完成再重算一次装饰
      const kick = () => {
        if (loadKicked || window.hljs) return;
        if (!hasLangBlock(editorView.state.doc)) return;
        loadKicked = true;
        loadHljs()
          .then(() => editorView.dispatch(editorView.state.tr.setMeta(key, true)))
          .catch(() => {}); // 加载失败保持素颜
      };
      kick();
      return {
        update: kick, // 编辑中新出现语言标注时触发加载
      };
    },
    props: {
      decorations(state) {
        return key.getState(state);
      },
    },
  });
});

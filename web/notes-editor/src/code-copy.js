// 代码块一键复制 —— code_block NodeView：
//   div.ne-codeblock（定位锚点） > button.ne-code-copy + pre > code(contentDOM)
// 按钮必须挂在 pre 外层的 wrapper 上：pre 是 overflow-x:auto 滚动容器，
// absolute 元素会跟内容一起横向滚走；wrapper 不滚，按钮钉死右上角。
// 复制走 navigator.clipboard，非安全上下文（生产是 http）回退隐藏
// textarea + execCommand；成功后图标变对勾 1.5s。编辑/只读/分屏预览通用。
import { codeBlockSchema } from '@milkdown/kit/preset/commonmark';
import { $view } from '@milkdown/kit/utils';

const COPY_ICON =
  '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.5">' +
  '<rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/>' +
  '<path d="M10.5 5.5v-2a1.5 1.5 0 0 0-1.5-1.5H4A1.5 1.5 0 0 0 2.5 3.5v5A1.5 1.5 0 0 0 4 10h1.5"/></svg>';
const OK_ICON =
  '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.8">' +
  '<path d="M3 8.5l3.5 3.5L13 5"/></svg>';

function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text);
  }
  return new Promise((resolve, reject) => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand('copy') ? resolve() : reject(new Error('copy failed'));
    } catch (e) {
      reject(e);
    } finally {
      ta.remove();
    }
  });
}

export const codeCopyView = $view(codeBlockSchema.node, () => (initialNode) => {
  let currentNode = initialNode;

  const dom = document.createElement('div');
  dom.className = 'ne-codeblock';
  const pre = document.createElement('pre');
  const code = document.createElement('code');
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'ne-code-copy';
  btn.title = '复制代码';
  btn.setAttribute('aria-label', '复制代码');
  btn.contentEditable = 'false';
  btn.innerHTML = COPY_ICON;
  pre.appendChild(code);
  dom.append(btn, pre);

  const syncAttrs = (node) => {
    const lang = (node.attrs.language || '').trim();
    if (lang) pre.dataset.language = lang;
    else delete pre.dataset.language;
  };
  syncAttrs(initialNode);

  let resetTimer = null;
  // mousedown 就拦掉：不让点击落进 ProseMirror 造成选区/焦点跳动
  btn.addEventListener('mousedown', (e) => e.preventDefault());
  btn.addEventListener('click', () => {
    copyText(currentNode.textContent).then(() => {
      btn.innerHTML = OK_ICON;
      btn.classList.add('ne-copied');
      btn.title = '已复制';
      clearTimeout(resetTimer);
      resetTimer = setTimeout(() => {
        btn.innerHTML = COPY_ICON;
        btn.classList.remove('ne-copied');
        btn.title = '复制代码';
      }, 1500);
    }).catch(() => {});
  });

  return {
    dom,
    contentDOM: code,
    update: (node) => {
      if (node.type !== initialNode.type) return false;
      currentNode = node;
      syncAttrs(node);
      return true;
    },
    stopEvent: (event) => btn.contains(event.target),
    ignoreMutation: (mutation) => btn.contains(mutation.target),
    destroy: () => clearTimeout(resetTimer),
  };
});

// [TOC] 目录渲染 —— ProseMirror 装饰实现,不改文档内容(序列化仍是 [TOC] 原文,
// Editor.md/Typora 等工具照常识别)。内容恰为 [TOC](不分大小写)的顶层段落:
// 原文隐藏(node 装饰 .ne-toc-p),原位放 widget 目录块,点击平滑滚到对应标题。
// 标题只扫顶层块——与侧栏大纲/滚动同步的口径一致(Markdown 标题本就在顶层)。
import { $prose } from '@milkdown/kit/utils';
import { Plugin, PluginKey } from '@milkdown/kit/prose/state';
import { Decoration, DecorationSet } from '@milkdown/kit/prose/view';

const key = new PluginKey('notes-toc');

function isTocPara(node) {
  return node.type.name === 'paragraph' && node.textContent.trim().toLowerCase() === '[toc]';
}

function renderToc(headings, getView) {
  const nav = document.createElement('nav');
  nav.className = 'ne-toc-block';
  nav.contentEditable = 'false';
  if (!headings.length) {
    const empty = document.createElement('span');
    empty.className = 'ne-toc-empty';
    empty.textContent = '目录 — 文档还没有标题';
    nav.appendChild(empty);
    return nav;
  }
  const min = Math.min(...headings.map((h) => h.level));
  for (const h of headings) {
    const a = document.createElement('a');
    a.className = 'ne-toc-item';
    a.style.paddingLeft = (h.level - min) * 16 + 'px';
    a.textContent = h.text;
    a.addEventListener('click', (e) => {
      e.preventDefault();
      const view = getView();
      if (!view) return;
      const dom = view.nodeDOM(h.pos);
      if (dom && dom.scrollIntoView) dom.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
    nav.appendChild(a);
  }
  return nav;
}

function buildDecorations(doc, getView) {
  const headings = [];
  const tocs = [];
  doc.forEach((node, offset) => {
    if (node.type.name === 'heading' && node.textContent.trim()) {
      headings.push({ level: node.attrs.level || 1, text: node.textContent.trim(), pos: offset });
    } else if (isTocPara(node)) {
      tocs.push({ pos: offset, size: node.nodeSize });
    }
  });
  if (!tocs.length) return DecorationSet.empty;
  const decos = [];
  for (const t of tocs) {
    decos.push(Decoration.node(t.pos, t.pos + t.size, { class: 'ne-toc-p' }));
    decos.push(Decoration.widget(t.pos, () => renderToc(headings, getView), { side: -1 }));
  }
  return DecorationSet.create(doc, decos);
}

export const tocPlugin = $prose(() => {
  let viewRef = null;
  const getView = () => viewRef;
  return new Plugin({
    key,
    state: {
      init(_, { doc }) {
        return buildDecorations(doc, getView);
      },
      apply(tr, old, _oldState, newState) {
        if (!tr.docChanged) return old.map(tr.mapping, tr.doc);
        return buildDecorations(newState.doc, getView);
      },
    },
    view(editorView) {
      viewRef = editorView;
      return { destroy() { viewRef = null; } };
    },
    props: {
      decorations(state) {
        return key.getState(state);
      },
    },
  });
});

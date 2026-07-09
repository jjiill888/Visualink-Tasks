// GFM 任务列表复选框 —— preset-gfm 只给 li 加 data-checked 属性，
// 真实的 checkbox 渲染是主题（Crepe）的职责，本项目不用主题，
// 这里用 NodeView 自己画：li 内前置不可编辑的 label+checkbox，
// 正文放进 .ne-task-body（CSS 里 li 是 flex 布局）。
// 勾选切换走 setNodeMarkup 改 checked 属性 → markdownUpdated → 自动保存。
// 只读页/分屏预览（contenteditable=false）由 CSS 关掉指针事件。
import { listItemSchema } from '@milkdown/kit/preset/commonmark';
import { $view } from '@milkdown/kit/utils';

export const taskListItemView = $view(listItemSchema.node, () => (initialNode, view, getPos) => {
  const isTask = initialNode.attrs.checked != null;

  const dom = document.createElement('li');
  const syncAttrs = (node) => {
    dom.dataset.label = node.attrs.label;
    dom.dataset.listType = node.attrs.listType;
    dom.dataset.spread = node.attrs.spread;
    if (isTask) {
      dom.dataset.itemType = 'task';
      dom.dataset.checked = node.attrs.checked;
    }
  };
  syncAttrs(initialNode);

  // 普通列表项：维持原 toDOM 的结构（li 本身即内容容器）
  if (!isTask) {
    return {
      dom,
      contentDOM: dom,
      update: (node) => {
        if (node.type !== initialNode.type || node.attrs.checked != null) return false;
        syncAttrs(node);
        return true;
      },
    };
  }

  let currentNode = initialNode;
  const label = document.createElement('label');
  label.contentEditable = 'false';
  const input = document.createElement('input');
  input.type = 'checkbox';
  input.checked = initialNode.attrs.checked;
  label.appendChild(input);

  const body = document.createElement('div');
  body.className = 'ne-task-body';
  dom.append(label, body);

  input.addEventListener('change', () => {
    const pos = typeof getPos === 'function' ? getPos() : null;
    if (pos == null || !view.editable) {
      input.checked = currentNode.attrs.checked; // 只读态兜底回弹
      return;
    }
    view.dispatch(view.state.tr.setNodeMarkup(pos, undefined, {
      ...currentNode.attrs,
      checked: input.checked,
    }));
  });

  return {
    dom,
    contentDOM: body,
    update: (node) => {
      if (node.type !== initialNode.type || node.attrs.checked == null) return false;
      currentNode = node;
      syncAttrs(node);
      input.checked = node.attrs.checked;
      return true;
    },
    stopEvent: (event) => label.contains(event.target),
    ignoreMutation: (mutation) => label.contains(mutation.target),
  };
});

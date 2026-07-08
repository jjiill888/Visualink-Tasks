// 表格操作工具条 —— 原生 JS 实现，替代官方 tableBlock 组件
//（tableBlock 内部用 Vue 渲染，拖进约 113KB 依赖；本文件 + CSS 功能等价：
//  加/删行列、三向对齐、删表，只是没有拖拽排序手柄）。
// 行为：光标进入表格时，表格上方浮出一条低调的按钮工具条；离开即隐藏。
// 加删行列用 prosemirror-tables 原生命令；对齐用 GFM 预设的 setAlignCommand
//（列级对齐，序列化回 | :-: | 语法）。
import { commandsCtx } from '@milkdown/kit/core';
import { $prose } from '@milkdown/kit/utils';
import { Plugin } from '@milkdown/kit/prose/state';
import {
  isInTable,
  addRowAfter,
  addColumnAfter,
  deleteRow,
  deleteColumn,
  deleteTable,
} from '@milkdown/kit/prose/tables';
import { setAlignCommand } from '@milkdown/kit/preset/gfm';

const svg = (paths) =>
  `<svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round">${paths}</svg>`;

const ICONS = {
  addRow: svg('<rect x="2.5" y="3" width="11" height="5" rx="1"/><path d="M8 10.5v3.5M6.2 12.3h3.6"/>'),
  addCol: svg('<rect x="3" y="2.5" width="5" height="11" rx="1"/><path d="M10.5 8h3.5M12.3 6.2v3.6"/>'),
  delRow: svg('<rect x="2.5" y="3" width="11" height="5" rx="1"/><path d="M6.2 12.3h3.6"/>'),
  delCol: svg('<rect x="3" y="2.5" width="5" height="11" rx="1"/><path d="M10.5 8h3.5"/>'),
  alignLeft: svg('<path d="M3 4h10M3 8h6M3 12h10"/>'),
  alignCenter: svg('<path d="M3 4h10M5 8h6M3 12h10"/>'),
  alignRight: svg('<path d="M3 4h10M7 8h6M3 12h10"/>'),
  delTable: svg('<path d="M4 4l8 8M12 4l-8 8"/>'),
};

// 光标所在的最近 table 节点（返回文档位置，找不到返回 -1）
function findTablePos(state) {
  const $from = state.selection.$from;
  for (let d = $from.depth; d > 0; d--) {
    if ($from.node(d).type.name === 'table') return $from.before(d);
  }
  return -1;
}

export const tableToolbar = $prose((ctx) => {
  return new Plugin({
    view: (editorView) => {
      if (!editorView.editable) return {}; // 只读页（历史版本）不挂工具条

      const bar = document.createElement('div');
      bar.className = 'ne-table-toolbar';
      bar.style.display = 'none';

      const buttons = [
        ['addRow', '在下方插入行', (view) => addRowAfter(view.state, view.dispatch)],
        ['addCol', '在右侧插入列', (view) => addColumnAfter(view.state, view.dispatch)],
        ['delRow', '删除当前行', (view) => deleteRow(view.state, view.dispatch)],
        ['delCol', '删除当前列', (view) => deleteColumn(view.state, view.dispatch)],
        ['alignLeft', '本列左对齐', () => ctx.get(commandsCtx).call(setAlignCommand.key, 'left')],
        ['alignCenter', '本列居中', () => ctx.get(commandsCtx).call(setAlignCommand.key, 'center')],
        ['alignRight', '本列右对齐', () => ctx.get(commandsCtx).call(setAlignCommand.key, 'right')],
        ['delTable', '删除表格', (view) => deleteTable(view.state, view.dispatch)],
      ];
      for (const [icon, title, run] of buttons) {
        const b = document.createElement('button');
        b.type = 'button';
        b.title = title;
        b.innerHTML = ICONS[icon];
        // mousedown 阻止默认行为，保住编辑器里的光标/选区
        b.addEventListener('mousedown', (e) => e.preventDefault());
        b.addEventListener('click', () => {
          run(editorView);
          editorView.focus();
        });
        bar.appendChild(b);
        if (icon === 'delCol' || icon === 'alignRight') {
          const sep = document.createElement('span');
          sep.className = 'ne-tt-sep';
          bar.appendChild(sep);
        }
      }
      document.body.appendChild(bar);

      const update = (view) => {
        if (!isInTable(view.state)) {
          bar.style.display = 'none';
          return;
        }
        const pos = findTablePos(view.state);
        if (pos < 0) {
          bar.style.display = 'none';
          return;
        }
        const dom = view.nodeDOM(pos);
        if (!dom || !dom.getBoundingClientRect) {
          bar.style.display = 'none';
          return;
        }
        const rect = dom.getBoundingClientRect();
        bar.style.display = 'flex';
        // 文档坐标定位（随页面滚动），置于表格左上方
        bar.style.top = `${rect.top + window.scrollY - 34}px`;
        bar.style.left = `${rect.left + window.scrollX}px`;
      };

      update(editorView);
      return {
        update,
        destroy: () => bar.remove(),
      };
    },
  });
});

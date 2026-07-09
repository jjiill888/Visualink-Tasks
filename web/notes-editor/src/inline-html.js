// 行内 HTML 白名单渲染 —— 补齐 Editor.md 系文档里常见的行内标签。
// Milkdown 的 commonmark preset 把行内 HTML 统一当原子节点显示为源码
// （span[data-type=html]），导致 <s>删除线</s>、上标 X<sup>2</sup>、
// 下标 O<sub>2</sub>、<kbd>/<mark>/<u> 等全部渲染成裸标签文字。
//
// 做法：remark 解析后做一次 mdast 变换，把成对的白名单行内标签
// （开/闭是两个独立的 html 节点，夹着中间的兄弟节点）折叠成自定义
// inlineTag 节点，再由对应的 ProseMirror mark 渲染成真实元素：
//   <s>/<del>/<strike>  → 直接转 mdast delete，复用 GFM 删除线 mark
//                          （序列化回 ~~...~~，语义等价）
//   <br>/<br/>          → 转 mdast break，复用 hardbreak 节点
//   <sup>/<sub>/<u>/<ins>/<kbd>/<mark> → inlineTag 节点 + 同名 mark，
//                          序列化时原样写回 <tag>...</tag>
//   <abbr title="全称">   → 唯一放行属性的标签（仅 title），悬停显示全称
// 其余带属性的标签、跨段落的标签对不处理，保持源码原样显示（不吞内容）。
import { InitReady, remarkPluginsCtx } from '@milkdown/kit/core';
import { $markSchema } from '@milkdown/kit/utils';

// 有独立 mark 的标签（渲染 = 浏览器同名元素默认样式 + notes-editor.css）
const MARK_TAGS = ['sup', 'sub', 'u', 'ins', 'kbd', 'mark'];
// 转成 GFM delete 的删除线别名标签
const STRIKE_TAGS = ['s', 'del', 'strike'];

const ALL_TAGS = [...MARK_TAGS, ...STRIKE_TAGS, 'abbr'];
const OPEN_RE = new RegExp(`^<(${ALL_TAGS.join('|')})>$`, 'i');
const CLOSE_RE = new RegExp(`^</(${ALL_TAGS.join('|')})>$`, 'i');
const BR_RE = /^<br\s*\/?>$/i;
// abbr 是唯一放行属性的标签，且只放行 title（双/单引号皆可）
const ABBR_OPEN_RE = /^<abbr(?:\s+title\s*=\s*(?:"([^"]*)"|'([^']*)'))?\s*>$/i;

// title 属性值里的常见实体还原（写回时再转义，保证往返一致）
function decodeEntities(s) {
  return s
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&');
}

// 开标签匹配：返回 { tag, title? }，不匹配返回 null
function matchOpen(v) {
  const m = OPEN_RE.exec(v);
  if (m) return { tag: m[1].toLowerCase() };
  const a = ABBR_OPEN_RE.exec(v);
  if (a) return { tag: 'abbr', title: decodeEntities(a[1] ?? a[2] ?? '') };
  return null;
}

// 在同层兄弟节点里配对白名单标签；配上的折叠成 inlineTag / delete，
// 内层再递归配对（支持 <sup><u>x</u></sup> 这类嵌套）
function pairChildren(children, parent) {
  // root 级的 <br /> 是 Milkdown 空段落的序列化标记（见 preset-commonmark
  // 的 remark-preserve-empty-line），留给原机制处理，只转正文里的 br
  const brToBreak = parent.type !== 'root';
  const out = [];
  for (let i = 0; i < children.length; i++) {
    const node = children[i];
    if (node.type === 'html' && typeof node.value === 'string') {
      const v = node.value.trim();
      if (brToBreak && BR_RE.test(v)) {
        out.push({ type: 'break' });
        continue;
      }
      const open = matchOpen(v);
      if (open) {
        const tag = open.tag;
        // 找同名闭合标签，遇到嵌套的同名开标签要跳过对应闭合
        let depth = 0;
        let close = -1;
        for (let j = i + 1; j < children.length; j++) {
          const sib = children[j];
          if (sib.type !== 'html' || typeof sib.value !== 'string') continue;
          const sv = sib.value.trim();
          const o = matchOpen(sv);
          const c = CLOSE_RE.exec(sv);
          if (o && o.tag === tag) depth++;
          else if (c && c[1].toLowerCase() === tag) {
            if (depth === 0) { close = j; break; }
            depth--;
          }
        }
        if (close > -1) {
          const inner = pairChildren(children.slice(i + 1, close), parent);
          out.push(
            STRIKE_TAGS.includes(tag)
              ? { type: 'delete', children: inner }
              : { type: 'inlineTag', tag, title: open.title, children: inner }
          );
          i = close;
          continue;
        }
      }
    }
    out.push(node);
  }
  return out;
}

function walk(node) {
  if (!Array.isArray(node.children)) return;
  node.children = pairChildren(node.children, node);
  for (const child of node.children) walk(child);
}

// 序列化：inlineTag 原样写回 <tag>...</tag>（delete/break 由
// remark-gfm / remark-stringify 内建处理）
function inlineTagToMarkdown(node, _parent, state, info) {
  const title = node.title
    ? ` title="${String(node.title).replace(/&/g, '&amp;').replace(/"/g, '&quot;')}"`
    : '';
  const open = `<${node.tag}${title}>`;
  return (
    open +
    state.containerPhrasing(node, { ...info, before: open, after: '<' }) +
    `</${node.tag}>`
  );
}

export function remarkInlineHtmlLite() {
  const data = this.data();
  const extensions = data.toMarkdownExtensions || (data.toMarkdownExtensions = []);
  if (!extensions.some((e) => e.handlers && e.handlers.inlineTag)) {
    extensions.push({ handlers: { inlineTag: inlineTagToMarkdown } });
  }
  return (tree) => {
    walk(tree);
    return tree;
  };
}

// 不用 $remark（追加注册）：preset-commonmark 的 remark-preserve-empty-line
// 会把树里所有 <br /> 删光（它是 Milkdown 的空行标记回收机制），正文里
// 真正的换行 <br /> 也被误删。这里手动「前插」到 remarkPluginsCtx，
// 保证本转换先于 preset 的所有 remark transform 运行。
const remarkInlineHtmlPlugin = (ctx) => async () => {
  await ctx.wait(InitReady);
  const remarkPlugin = { plugin: remarkInlineHtmlLite, options: {} };
  ctx.update(remarkPluginsCtx, (rp) => [remarkPlugin, ...rp]);
  return () => {
    ctx.update(remarkPluginsCtx, (rp) => rp.filter((x) => x !== remarkPlugin));
  };
};

const inlineTagMarks = MARK_TAGS.map((tag) =>
  $markSchema(`html_${tag}`, () => ({
    parseDOM: [{ tag }],
    toDOM: () => [tag],
    parseMarkdown: {
      match: (node) => node.type === 'inlineTag' && node.tag === tag,
      runner: (state, node, markType) => {
        state.openMark(markType);
        state.next(node.children);
        state.closeMark(markType);
      },
    },
    toMarkdown: {
      match: (mark) => mark.type.name === `html_${tag}`,
      runner: (state, mark) => {
        state.withMark(mark, 'inlineTag', undefined, { tag });
      },
    },
  }))
);

// abbr 单独建 mark：多一个 title 属性（悬停显示全称），写回时带 title
const abbrMark = $markSchema('html_abbr', () => ({
  attrs: { title: { default: '' } },
  parseDOM: [{
    tag: 'abbr',
    getAttrs: (dom) => ({ title: dom.getAttribute('title') || '' }),
  }],
  toDOM: (mark) => ['abbr', { title: mark.attrs.title || undefined }],
  parseMarkdown: {
    match: (node) => node.type === 'inlineTag' && node.tag === 'abbr',
    runner: (state, node, markType) => {
      state.openMark(markType, { title: node.title || '' });
      state.next(node.children);
      state.closeMark(markType);
    },
  },
  toMarkdown: {
    match: (mark) => mark.type.name === 'html_abbr',
    runner: (state, mark) => {
      state.withMark(mark, 'inlineTag', undefined, { tag: 'abbr', title: mark.attrs.title });
    },
  },
}));

export const inlineHtml = [remarkInlineHtmlPlugin, inlineTagMarks, abbrMark].flat(2);

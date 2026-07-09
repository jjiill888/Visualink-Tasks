// Markdown 兼容增强（对照 Editor.md 全语法测试模板补齐）：
//   1. 数学公式：remark-math 解析 $...$ / $$...$$，KaTeX 渲染
//      （@milkdown/plugin-math 已停更于 7.5.x，与 kit 7.21 不兼容，故自实现轻量节点）
//      Editor.md 系的围栏式公式块 ```math / ```katex / ```latex / ```tex
//      也转成公式块渲染（fence 记在节点属性里，保存时原样写回同语言围栏）；
//      行内 $\(...\)$ / 块级 \[...\] 的包裹符剥掉（KaTeX 不认 \( \)，会红字报错）
//   2. Emoji 短代码：自写轻量映射（见 emoji-lite.js，替代拖入 213KB 数据库的 remark-emoji）
//   3. YAML front matter：remark-frontmatter + yaml 节点，防止 ---...--- 被误解析成
//      setext 标题导致保存时内容损坏；显示为低调的预格式块，可直接编辑
// KaTeX 不进主 bundle（跨境省 62KB brotli）：JS/CSS/字体自托管在
// static/vendor/katex/，文档里出现公式时才按需注入加载（URL 由模板经
// window.__notesKatex 提供，带缓存版本参数）。
import remarkMath from 'remark-math';
import remarkFrontmatter from 'remark-frontmatter';
import { $remark, $nodeSchema, $prose } from '@milkdown/kit/utils';
import { Plugin } from '@milkdown/kit/prose/state';

import remarkEmojiLite from './emoji-lite.js';

// ── KaTeX 按需加载 ──
let katexLoading = null;

function loadKatex() {
  if (window.katex) return Promise.resolve(window.katex);
  if (!katexLoading) {
    const cfg = window.__notesKatex || {};
    katexLoading = new Promise((resolve, reject) => {
      if (cfg.css) {
        const link = document.createElement('link');
        link.rel = 'stylesheet';
        link.href = cfg.css;
        document.head.appendChild(link);
      }
      const s = document.createElement('script');
      s.src = cfg.js || '/static/vendor/katex/katex.min.js';
      s.onload = () => resolve(window.katex);
      s.onerror = () => reject(new Error('katex load failed'));
      document.head.appendChild(s);
    });
  }
  return katexLoading;
}

function renderKatex(el, value, displayMode) {
  // throwOnError:false —— 语法错误的公式以红色原文显示，不中断渲染
  if (window.katex) {
    window.katex.render(value, el, { throwOnError: false, displayMode });
    return;
  }
  // 加载期间先显示源码占位；失败保持源码（内容不丢）
  el.textContent = value;
  loadKatex()
    .then((k) => k.render(value, el, { throwOnError: false, displayMode }))
    .catch(() => {});
}

// ── 围栏式公式块 + 公式内容归一 ──
const MATH_FENCE_LANGS = ['math', 'katex', 'latex', 'tex'];

// 剥掉 \(...\) / \[...\] 包裹（Editor.md 模板写法，KaTeX 不认这两个定界符）
function normalizeMathValue(v) {
  const s = v.trim();
  if (s.startsWith('\\(') && s.endsWith('\\)')) return s.slice(2, -2).trim();
  if (s.startsWith('\\[') && s.endsWith('\\]')) return s.slice(2, -2).trim();
  return s;
}

// ```math 等围栏代码块转 math 节点（fenceLang 记住围栏语言，序列化原样写回）。
// 内容里的 \_ 还原成 _：Editor.md 的模板经过其 markdown 管线会先解转义，
// 我们的围栏内容是逐字的，\_ 会被 KaTeX 渲染成下划线字符而非下标
function remarkMathFences() {
  return (tree) => {
    (function walk(node) {
      if (!Array.isArray(node.children)) return;
      for (let i = 0; i < node.children.length; i++) {
        const child = node.children[i];
        if (child.type === 'code' && MATH_FENCE_LANGS.includes((child.lang || '').toLowerCase())) {
          node.children[i] = {
            type: 'math',
            fenceLang: child.lang.toLowerCase(),
            value: child.value.replace(/\\_/g, '_').trim(),
          };
          continue;
        }
        if (child.type === 'math' || child.type === 'inlineMath') {
          child.value = normalizeMathValue(child.value || '');
          continue;
        }
        walk(child);
      }
    })(tree);
  };
}

const remarkMathPlugin = $remark('remarkMath', () => remarkMath);
const remarkMathFencesPlugin = $remark('remarkMathFences', () => remarkMathFences);
const remarkEmojiPlugin = $remark('remarkEmojiLite', () => remarkEmojiLite);
// 注意：$remark 不传 initialOptions 时会给插件塞空对象 {}，
// 而 remark-frontmatter 要求 {} 里必须有 type 字段 —— 显式传 'yaml' 预设
const remarkFrontmatterPlugin = $remark('remarkFrontmatter', () => remarkFrontmatter, 'yaml');

// 行内公式 $x$ / $$x$$（remark-math 的 inlineMath 节点）
const mathInlineSchema = $nodeSchema('math_inline', () => ({
  group: 'inline',
  inline: true,
  atom: true,
  attrs: { value: { default: '' } },
  parseDOM: [{
    tag: 'span[data-type="math-inline"]',
    getAttrs: (dom) => ({ value: dom.dataset.value || '' }),
  }],
  toDOM: (node) => {
    const span = document.createElement('span');
    span.dataset.type = 'math-inline';
    span.dataset.value = node.attrs.value;
    span.title = '双击编辑公式';
    renderKatex(span, node.attrs.value, false);
    return span;
  },
  parseMarkdown: {
    match: (node) => node.type === 'inlineMath',
    runner: (state, node, type) => {
      state.addNode(type, { value: node.value });
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'math_inline',
    runner: (state, node) => {
      state.addNode('inlineMath', undefined, node.attrs.value);
    },
  },
}));

// 块级公式：$$...$$（remark-math 的 math 节点）或 ```math 系围栏
// （remarkMathFences 转换，fence 属性记住围栏语言，保存时原样写回）
const mathBlockSchema = $nodeSchema('math_block', () => ({
  group: 'block',
  atom: true,
  marks: '',
  attrs: { value: { default: '' }, fence: { default: '' } },
  parseDOM: [{
    tag: 'div[data-type="math-block"]',
    getAttrs: (dom) => ({ value: dom.dataset.value || '', fence: dom.dataset.fence || '' }),
  }],
  toDOM: (node) => {
    const div = document.createElement('div');
    div.dataset.type = 'math-block';
    div.dataset.value = node.attrs.value;
    if (node.attrs.fence) div.dataset.fence = node.attrs.fence;
    div.title = '双击编辑公式';
    renderKatex(div, node.attrs.value, true);
    return div;
  },
  parseMarkdown: {
    match: (node) => node.type === 'math',
    runner: (state, node, type) => {
      state.addNode(type, { value: node.value, fence: node.fenceLang || '' });
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'math_block',
    runner: (state, node) => {
      if (node.attrs.fence) {
        state.addNode('code', undefined, node.attrs.value, { lang: node.attrs.fence });
      } else {
        state.addNode('math', undefined, node.attrs.value);
      }
    },
  },
}));

// 公式是原子节点，不能在正文里直接改：双击弹出输入框编辑 LaTeX 源码
const mathEditPlugin = $prose(() => new Plugin({
  props: {
    handleDoubleClickOn(view, _pos, node, nodePos) {
      if (node.type.name !== 'math_inline' && node.type.name !== 'math_block') return false;
      const next = window.prompt('编辑公式（LaTeX）：', node.attrs.value);
      if (next !== null && next !== node.attrs.value) {
        view.dispatch(view.state.tr.setNodeMarkup(nodePos, undefined, { ...node.attrs, value: next }));
      }
      return true;
    },
  },
}));

// YAML front matter（文档最顶部的 --- ... ---），显示为可编辑的预格式块
const frontmatterSchema = $nodeSchema('yaml', () => ({
  content: 'text*',
  group: 'block',
  marks: '',
  code: true,
  defining: true,
  parseDOM: [{ tag: 'pre[data-type="frontmatter"]', preserveWhitespace: 'full' }],
  toDOM: () => ['pre', { 'data-type': 'frontmatter', class: 'note-frontmatter' }, ['code', 0]],
  parseMarkdown: {
    match: (node) => node.type === 'yaml',
    runner: (state, node, type) => {
      state.openNode(type);
      if (node.value) state.addText(node.value);
      state.closeNode();
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'yaml',
    runner: (state, node) => {
      state.addNode('yaml', undefined, node.textContent);
    },
  },
}));

// 供 main.js 一次性 .use() 的插件集合
export const markdownExtra = [
  remarkMathPlugin,
  remarkMathFencesPlugin,
  remarkEmojiPlugin,
  remarkFrontmatterPlugin,
  mathInlineSchema,
  mathBlockSchema,
  mathEditPlugin,
  frontmatterSchema,
].flat();

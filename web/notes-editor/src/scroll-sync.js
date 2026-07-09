// 「源码/预览」分屏的滚动与光标同步。
//
// 设计：不用滚动百分比（两侧高度不成比例，代码块/图片/表格会越滚越偏），
// 而是「块锚点 + 分段线性插值」：
//   1. 左右两侧来自同一份 Markdown——用 remark（与编辑器同一套解析生态）
//      解析出每个顶层块的源码起始 offset；
//   2. 左侧：隐藏镜像 div 复刻 textarea 的排版（同字体/宽度/换行），
//      逐块包 <span> 读 offsetTop，得到每块在 textarea 内容里的真实像素 y
//      ——软换行的长段落也准确；
//   3. 右侧：取 ProseMirror 顶层节点相对滚动容器的 y；
//   4. 两组 y 配成锚点对，双向滚动都在锚点间做分段线性插值。
// 光标跟随：镜像 div 里放 marker span 精确量出光标像素 y，映射到预览侧，
// 目标不在预览可视区中部时才滚动（避免每次击键都微调）。
// 回环抑制：程序性 setScrollTop 会触发对侧 scroll 事件，用 lock 标记来源，
// 平滑滚动动画期间锁窗口放宽到 700ms。
import { unified } from 'unified';
import remarkParse from 'remark-parse';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import remarkFrontmatter from 'remark-frontmatter';

// 只做解析取位置信息，插件集合对齐编辑器管线（gfm 表格、数学块、frontmatter
// 都影响顶层块边界），保证块数与 ProseMirror 顶层节点尽量一一对应
const processor = unified()
  .use(remarkParse)
  .use(remarkGfm)
  .use(remarkMath)
  .use(remarkFrontmatter, ['yaml']);

function escapeHTML(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;');
}

// 分段线性插值：xs 单调递增，v 落在哪段就按比例映射到 ys 对应段
function interp(v, xs, ys) {
  const last = xs.length - 1;
  if (last < 0) return 0;
  if (v <= xs[0]) return ys[0];
  if (v >= xs[last]) return ys[last];
  let lo = 0;
  let hi = last;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if (xs[mid] <= v) lo = mid;
    else hi = mid;
  }
  const t = (v - xs[lo]) / (xs[hi] - xs[lo] || 1);
  return ys[lo] + t * (ys[hi] - ys[lo]);
}

export function createScrollSync({ srcEl, previewEl, getPmRoot }) {
  let active = false;
  let mirror = null;
  let srcYs = [0];
  let prevYs = [0];
  let prevHeightCache = 0; // 预览高度变化（hljs/KaTeX/图片迟到）时懒重建锚点
  let lock = null;
  let lockTimer = null;
  let resizeTimer = null;

  function lockBy(who, ms) {
    lock = who;
    clearTimeout(lockTimer);
    lockTimer = setTimeout(() => { lock = null; }, ms || 150);
  }

  function ensureMirror() {
    if (mirror) return;
    mirror = document.createElement('div');
    mirror.setAttribute('aria-hidden', 'true');
    mirror.style.cssText =
      'position:absolute;left:-99999px;top:0;visibility:hidden;pointer-events:none;' +
      'box-sizing:border-box;white-space:pre-wrap;overflow-wrap:break-word;';
    document.body.appendChild(mirror);
  }

  // 镜像必须与 textarea 同宽同字体，分栏宽度随窗口变，每次重建前同步
  function syncMirrorStyle() {
    const cs = getComputedStyle(srcEl);
    for (const p of ['fontFamily', 'fontSize', 'fontWeight', 'lineHeight',
      'letterSpacing', 'tabSize', 'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft']) {
      mirror.style[p] = cs[p];
    }
    mirror.style.width = srcEl.clientWidth + 'px';
  }

  function rebuild() {
    if (!active) return;
    const text = srcEl.value;
    let starts = [];
    try {
      starts = processor.parse(text).children
        .map((c) => c.position && c.position.start.offset)
        .filter((o) => typeof o === 'number');
    } catch (_) { /* 解析失败退化为只有首尾锚点（≈按比例滚动） */ }

    const pm = getPmRoot();
    // 顶层块节点；ProseMirror-widget/gapcursor 等装饰元素不算
    const kids = pm
      ? [...pm.children].filter((el) => !/(^|\s)ProseMirror-/.test(el.className || ''))
      : [];
    const n = Math.min(starts.length, kids.length); // 块数不齐时按序配对、多余的截掉

    ensureMirror();
    syncMirrorStyle();
    let html = '';
    for (let i = 0; i < n; i++) {
      if (i === 0 && starts[0] > 0) html += escapeHTML(text.slice(0, starts[0]));
      const to = i + 1 < n ? starts[i + 1] : text.length;
      html += '<span data-a>' + escapeHTML(text.slice(starts[i], to)) + '</span>';
    }
    mirror.innerHTML = html;

    const spans = mirror.querySelectorAll('[data-a]');
    const prevRect = previewEl.getBoundingClientRect();
    srcYs = [0];
    prevYs = [0];
    for (let i = 0; i < n; i++) {
      const sy = spans[i].offsetTop;
      const py = kids[i].getBoundingClientRect().top - prevRect.top + previewEl.scrollTop;
      // 单调性保护：异常配对（两侧块错位）宁可丢锚点也不产生回折
      if (sy > srcYs[srcYs.length - 1] && py > prevYs[prevYs.length - 1]) {
        srcYs.push(sy);
        prevYs.push(py);
      }
    }
    // 末端锚点：内容底对齐，保证尾部也映射到位
    srcYs.push(Math.max(srcEl.scrollHeight, srcYs[srcYs.length - 1] + 1));
    prevYs.push(Math.max(previewEl.scrollHeight, prevYs[prevYs.length - 1] + 1));
    prevHeightCache = previewEl.scrollHeight;
  }

  function rebuildIfHeightChanged() {
    if (previewEl.scrollHeight !== prevHeightCache) rebuild();
  }

  function onSrcScroll() {
    if (!active || lock === 'preview') return;
    lockBy('src');
    rebuildIfHeightChanged();
    previewEl.scrollTop = interp(srcEl.scrollTop, srcYs, prevYs);
  }

  function onPreviewScroll() {
    if (!active || lock === 'src') return;
    lockBy('preview');
    srcEl.scrollTop = interp(previewEl.scrollTop, prevYs, srcYs);
  }

  // 光标在 textarea 内容里的像素 y：镜像放光标前全文 + marker span
  function caretY() {
    ensureMirror();
    syncMirrorStyle();
    mirror.innerHTML = escapeHTML(srcEl.value.slice(0, srcEl.selectionStart))
      + '<span data-caret>​</span>';
    return mirror.querySelector('[data-caret]').offsetTop;
  }

  // 把光标对应的预览位置带进可视区：已在中部区间（25%~70%）就不动
  function syncToCaret() {
    if (!active) return;
    rebuildIfHeightChanged();
    const target = interp(caretY(), srcYs, prevYs);
    const vh = previewEl.clientHeight;
    const top = previewEl.scrollTop;
    if (target > top + vh * 0.25 && target < top + vh * 0.7) return;
    // 平滑滚动动画期间预览会持续发 scroll 事件，锁窗口放宽避免反向拽动源码
    lockBy('src', 700);
    previewEl.scrollTo({ top: Math.max(0, target - vh * 0.4), behavior: 'smooth' });
  }

  function onResize() {
    if (!active) return;
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(rebuild, 200);
  }

  srcEl.addEventListener('scroll', onSrcScroll);
  previewEl.addEventListener('scroll', onPreviewScroll);
  window.addEventListener('resize', onResize);

  return {
    enable() {
      active = true;
      rebuild();
    },
    disable() {
      active = false;
      lock = null;
    },
    // 预览内容更新（replaceAll）后调用：重建锚点并把光标位置带进预览
    previewChanged() {
      if (!active) return;
      rebuild();
      syncToCaret();
    },
    syncToCaret,
  };
}

// 阶段二：y-sweet 实时协作接线。
//
// 数据流（y-sweet 只在服务端内网，浏览器全程经 Go 反代）：
//   1. GET /notes/{id}/collab-token —— Go 校验权限后向 y-sweet 签发房间 token，
//      并把内网 ws 地址改写为 /collab 反代路径（YSweetProvider 的 authEndpoint
//      形态是「返回 ClientToken 的 async 函数」，token 过期重连时会自动再调）
//   2. YSweetProvider 连 ws，Yjs 文档 "note-{id}" 双向同步
//   3. 首次 synced 后把 Milkdown 编辑器绑进 CollabService：
//      bindDoc → setAwareness → applyTemplate(DB 快照) → connect
//
// 房间初始化（规格要求写明方案）：applyTemplate 采用 Milkdown 官方推荐做法——
// 同步完成后检查远端 Yjs 文档，为空才用 DB 的 content_md 填充，非空直接采用
// 远端内容。y-sweet 落盘持久化房间，所以只有全新房间的第一个连接者会填充。
// 已知竞态：两个客户端在同一毫秒级窗口内同时首连同一个全新房间时，可能各自
// 填充一次导致内容重复（CRDT 合并保留双方插入）。窗口极窄且只影响首次协作的
// 瞬间，重复内容删掉一份即可，不做分布式锁。
//
// 断线语义分两种（不要混淆）：
//   - 从未连上（服务挂了/token 拿不到）→ 8 秒超时降级：编辑器保持阶段一
//     单人模式（内容来自 defaultValue），提示「单人模式」，乐观锁照常
//   - 连上后中途断线 → 不降级：Yjs 改动本地暂存，provider 自动重连后补同步，
//     提示「协作离线，改动暂存本地」
import { Doc } from 'yjs';
import { YSweetProvider } from '@y-sweet/client';
import { collabServiceCtx } from '@milkdown/plugin-collab';

export { collab } from '@milkdown/plugin-collab';

// 协作者光标颜色：按用户名哈希从固定盘取色，同名恒同色（深浅主题都够对比度）
const CURSOR_COLORS = [
  '#e05252', '#d97706', '#059669', '#0284c7',
  '#7c3aed', '#db2777', '#65a30d', '#0891b2',
];

function colorFor(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return CURSOR_COLORS[h % CURSOR_COLORS.length];
}

// 启动协作。返回 { provider, doc, stop }；生命周期事件经回调通知 main.js：
//   onReady()          首次同步完成、编辑器已绑定（此后快照保存应跳过乐观锁）
//   onDegrade(reason)  从未连上，已放弃并降级单人模式
//   onStatus(text, level) 顶栏状态文案（level: '' | 'ok' | 'warn'）
export function startCollab({ editor, noteId, username, initialMarkdown, titleEl, onReady, onDegrade, onStatus }) {
  const doc = new Doc();
  const docId = 'note-' + noteId;
  let bound = false;     // 是否已把编辑器绑进协作服务（= 首次 synced 过）
  let degraded = false;

  const authEndpoint = async () => {
    const resp = await fetch('/notes/' + noteId + '/collab-token', { credentials: 'same-origin' });
    if (!resp.ok) throw new Error('collab-token ' + resp.status);
    return resp.json();
  };

  const provider = new YSweetProvider(authEndpoint, docId, doc, {
    connect: true,
    showDebuggerLink: false,
  });
  // 协作者身份：y-prosemirror 的远端光标从 awareness 的 user 字段取名字和颜色
  provider.awareness.setLocalStateField('user', { name: username, color: colorFor(username) });

  function onlineCount() {
    return provider.awareness.getStates().size;
  }

  // 状态栏只在有信息量时出现（极简原则，用户明确要求一个人时不显示任何字）：
  //   - 两人以上 → 「N 人在线」绿点
  //   - 一个人（协作正常/连接中/已降级）→ 空，什么都不显示
  //   - 连上后中途断线 → 「协作离线，改动暂存本地」——此时别人可能还在改，
  //     这是唯一保留的警示
  function refreshStatus() {
    if (degraded) {
      onStatus('', '');
      return;
    }
    if (provider.status === 'connected') {
      const n = onlineCount();
      onStatus(n > 1 ? n + ' 人在线' : '', 'ok');
    } else if (bound) {
      onStatus('协作离线，改动暂存本地', 'warn');
    } else {
      onStatus('', '');
    }
  }

  // 从未连上的降级窗口：8 秒内没完成首次同步就放弃（provider 会无限重试，
  // 不主动掐掉的话状态栏会永远「连接中」）。降级后本会话不再尝试协作。
  const degradeTimer = setTimeout(() => {
    if (bound || degraded) return;
    degraded = true;
    provider.disconnect();
    refreshStatus(); // 降级 = 清空状态栏（单人编辑不打扰）；并发保护回落到乐观锁 409
    onDegrade('connect-timeout');
  }, 8000);

  // 标题也走 Yjs（正文之外的独立字段）：不同步的话，一端改了名，另一端的
  // 快照保存仍带着旧标题，会把改名静默还原掉。
  // 关键：**不预填** yTitle——「空 = 沿用 DB 标题」，只有用户真正改名才写入。
  // 若像正文那样两端 sync 时都「空则填充」，同时首连会双重填充（CRDT 保留
  // 双方插入，标题变成两份拼接）。标题短且并发改名罕见，用整串替换即可。
  // 已知边角：把标题清空后新加入者仍显示 DB 旧标题（空串与「未改过」无法
  // 区分），实际编辑中清空标题即弃，影响可忽略。
  function bindTitle() {
    if (!titleEl) return;
    const yTitle = doc.getText('title');
    if (yTitle.length > 0) titleEl.value = yTitle.toString();
    yTitle.observe(() => {
      const v = yTitle.toString();
      if (v && titleEl.value !== v && document.activeElement !== titleEl) titleEl.value = v;
    });
    titleEl.addEventListener('input', () => {
      if (yTitle.toString() === titleEl.value) return;
      doc.transact(() => {
        yTitle.delete(0, yTitle.length);
        yTitle.insert(0, titleEl.value);
      });
    });
  }

  provider.on('synced', () => {
    if (bound || degraded) return;
    bound = true;
    clearTimeout(degradeTimer);
    editor.action((ctx) => {
      const service = ctx.get(collabServiceCtx);
      service
        .bindDoc(doc)
        .setAwareness(provider.awareness)
        .applyTemplate(initialMarkdown) // 空房间才填充 DB 快照，见文件头注释
        .connect();
    });
    bindTitle();
    refreshStatus();
    onReady();
  });

  provider.on('status', refreshStatus);
  provider.awareness.on('change', refreshStatus);
  refreshStatus();

  return {
    provider,
    doc,
    stop() {
      clearTimeout(degradeTimer);
      provider.disconnect();
    },
  };
}

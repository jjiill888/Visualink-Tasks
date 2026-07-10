// 阶段二：y-sweet 实时协作接线（本文件是懒加载 chunk，主 bundle 不背 yjs）。
//
// 数据流（可信内网性能优先，两种模式由服务端签发的 token 地址决定）：
//   - 直连模式（Y_SWEET_PUBLIC_PORT）：浏览器直连 y-sweet 暴露端口，Go 不在
//     数据路径上；裸连仍需有效房间 token（y-sweet 校验，伪 token 401）
//   - 反代模式：ws 走本服务 /collab 前缀（登录 session + token 双层）
// token 获取顺序：优先用服务端随页直出的预签 token（省一次 round-trip，
// ws 在 JS 解析完立即连）；过期/缺失时回退 GET /notes/{id}/collab-token
// （token 有效期 12h，服务端签发时指定）。
//
// 编辑器绑定用手动构建的 CollabService（不经 .use(collab) 插件注册）——
// 这是本文件能懒加载的关键：主 bundle 创建编辑器时无需知道协作存在。
//
// 房间初始化：首次 synced 后检查远端 Yjs 文档，为空才用**当前编辑器内容**
// 填充（不是页面初始快照——用户在连接窗口期敲的字不能丢），非空直接采用
// 远端内容。y-sweet 落盘持久化房间，只有全新房间的第一个连接者会填充。
// 已知竞态：两个客户端同毫秒首连同一全新房间可能各填一次导致内容重复
// （CRDT 保留双方插入），窗口极窄，重复删一份即可，不做分布式锁。
//
// 断线语义分两种（不要混淆）：
//   - 从未连上（服务挂/token 拿不到/8 秒超时）→ 降级：绑一个**本地** Yjs
//     文档（不连网），y-undo 撤销栈照常工作，保存走阶段一乐观锁
//   - 连上后中途断线 → 不降级：Yjs 改动本地暂存，provider 自动重连补同步
import { Doc } from 'yjs';
import { YSweetProvider } from '@y-sweet/client';
import { CollabService } from '@milkdown/plugin-collab';

// 协作者光标颜色：按用户名哈希从固定盘取色，同名恒同色。
// 低饱和五色系（与 UI 强调蓝同族），只用于 2px 光标竖线 + 小名牌，
// 绝不大面积铺背景——识别快且不破坏 Markdown 阅读
const CURSOR_COLORS = [
  '#6ea8fe', '#c58af9', '#63c174', '#e6a65d', '#e47785',
];

function colorFor(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return CURSOR_COLORS[h % CURSOR_COLORS.length];
}

// 启动协作。生命周期事件经回调通知 main.js：
//   onReady()             首次同步完成、编辑器已绑定（此后快照保存跳过乐观锁）
//   onDegrade(reason)     从未连上，已放弃并降级单人模式
//   onStatus(text, level) 顶栏状态文案（level: '' | 'ok' | 'warn'；空文案 = 隐藏）
//   onPeers(n)            房间在线人数变化（含自己）；0 = 连上后掉线、人数未知，
//                         调用方须按「不止自己」处理（源码分屏据此切换只读镜像）
//   onDocUpdate()         共享文档有更新（防抖 200ms 后回调，此时 y-prosemirror
//                         已把更新应用进编辑器视图）。Milkdown 的 listener 插件
//                         对远端事务不触发——y-prosemirror 给远端更新打了
//                         addToHistory:false（不进本地撤销栈），listener 一律跳过，
//                         所以源码镜像的刷新信号只能从 Yjs 这层取
export function startCollab({ editor, noteId, username, initialToken, currentMarkdown, titleEl, onReady, onDegrade, onStatus, onPeers, onDocUpdate }) {
  const doc = new Doc();
  const docId = 'note-' + noteId;
  let bound = false;     // 是否已把编辑器绑进协作服务（= 首次 synced 过）
  let degraded = false;

  const service = new CollabService();
  editor.action((ctx) => service.bindCtx(ctx));

  const authEndpoint = async () => {
    const resp = await fetch('/notes/' + noteId + '/collab-token', { credentials: 'same-origin' });
    if (!resp.ok) throw new Error('collab-token ' + resp.status);
    return resp.json();
  };

  const provider = new YSweetProvider(authEndpoint, docId, doc, {
    connect: true,
    showDebuggerLink: false,
    // 预签 token：跳过首次 auth 请求，直接开 ws
    initialClientToken: initialToken || undefined,
  });
  // 协作者身份：y-prosemirror 的远端光标从 awareness 的 user 字段取名字和颜色
  provider.awareness.setLocalStateField('user', { name: username, color: colorFor(username) });

  // 状态栏只在有信息量时出现（极简原则，用户明确要求一个人时不显示任何字）：
  //   - 两人以上 → 「N 人在线」绿点
  //   - 一个人（协作正常/连接中/已降级）→ 空，什么都不显示
  //   - 连上后中途断线 → 「协作离线，改动暂存本地」——此时别人可能还在改，
  //     这是唯一保留的警示
  function refreshStatus() {
    if (degraded) {
      onStatus('', '');
      if (onPeers) onPeers(1);
      return;
    }
    if (provider.status === 'connected') {
      const n = provider.awareness.getStates().size;
      onStatus(n > 1 ? n + ' 人在线' : '', 'ok');
      if (onPeers) onPeers(n);
    } else if (bound) {
      onStatus('协作离线，改动暂存本地', 'warn');
      if (onPeers) onPeers(0); // 掉线期间别人可能还在改，按人数未知处理
    } else {
      onStatus('', '');
      if (onPeers) onPeers(1);
    }
  }

  // 标题也走 Yjs（正文之外的独立字段）：不同步的话，一端改了名，另一端的
  // 快照保存仍带着旧标题，会把改名静默还原掉。
  // 关键：**不预填** yTitle——「空 = 沿用 DB 标题」，只有用户真正改名才写入。
  // 若像正文那样两端 sync 时都「空则填充」，同时首连会双重填充（CRDT 保留
  // 双方插入，标题变成两份拼接）。标题短且并发改名罕见，用整串替换即可。
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

  // 从未连上的降级窗口：8 秒内没完成首次同步就放弃（provider 会无限重试，
  // 不主动掐掉的话永远处于连接中）。降级后本会话不再尝试协作，但仍绑一个
  // 本地 Yjs 文档——y-undo 撤销栈（Mod-z 键位在 CollabService 里）照常工作，
  // 否则协作模式不装 prosemirror-history，降级后会彻底没有撤销。
  const degradeTimer = setTimeout(() => {
    if (bound || degraded) return;
    degraded = true;
    provider.disconnect();
    service
      .bindDoc(new Doc())               // 本地文档，不连网
      .applyTemplate(currentMarkdown()) // 空文档必填充，保留用户已敲内容
      .connect();
    refreshStatus();
    onDegrade('connect-timeout');
  }, 8000);

  provider.on('synced', () => {
    if (bound || degraded) return;
    bound = true;
    clearTimeout(degradeTimer);
    service
      .bindDoc(doc)
      .setAwareness(provider.awareness)
      // 空房间用**当前**编辑器内容填充（连接窗口期的输入不丢）；见文件头
      .applyTemplate(currentMarkdown())
      .connect();
    bindTitle();
    // 文档更新信号（远端/本地不区分，y-sweet client 应用远端更新不带 origin，
    // 区分不了；调用方按自身模式过滤，只在只读镜像时才重新序列化，开销可忽略）
    if (onDocUpdate) {
      let t = null;
      doc.on('update', () => {
        clearTimeout(t);
        t = setTimeout(onDocUpdate, 200);
      });
    }
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

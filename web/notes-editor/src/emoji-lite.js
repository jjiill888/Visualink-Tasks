// 轻量 emoji 短代码支持 —— 替代 remark-emoji。
// remark-emoji 会拖进完整的 emoji 名称数据库 emojilib（打包后 213KB，占
// bundle 约 20%），而 :smiley: 这类短代码只是 GitHub 系的输入习惯，中文
// 用户多用输入法直接打 emoji。这里用一张常用映射表（~2KB）+ 一个 mdast
// 文本节点替换器实现同样效果；不在表里的短代码原文保留。
// 表内容对齐 GitHub 常用短代码命名（node-emoji 同名）。

const EMOJI = {
  // 笑脸/情绪
  smile: '😄', smiley: '😃', grin: '😁', grinning: '😀', laughing: '😆',
  joy: '😂', rofl: '🤣', wink: '😉', blush: '😊', innocent: '😇',
  heart_eyes: '😍', kissing_heart: '😘', yum: '😋', stuck_out_tongue: '😛',
  stuck_out_tongue_winking_eye: '😜', zany_face: '🤪', thinking: '🤔',
  neutral_face: '😐', expressionless: '😑', unamused: '😒', roll_eyes: '🙄',
  smirk: '😏', grimacing: '😬', relieved: '😌', sleeping: '😴',
  sweat_smile: '😅', sweat: '😓', cold_sweat: '😰', cry: '😢', sob: '😭',
  angry: '😠', rage: '😡', scream: '😱', fearful: '😨', disappointed: '😞',
  confused: '😕', astonished: '😲', flushed: '😳', dizzy_face: '😵',
  mask: '😷', sunglasses: '😎', nerd_face: '🤓', smiling_imp: '😈',
  ghost: '👻', skull: '💀', alien: '👽', robot: '🤖', poop: '💩',
  // 手势/人
  wave: '👋', ok_hand: '👌', thumbsup: '👍', '+1': '👍', thumbsdown: '👎',
  '-1': '👎', clap: '👏', raised_hands: '🙌', pray: '🙏', muscle: '💪',
  point_up: '☝️', point_right: '👉', point_left: '👈', point_down: '👇',
  handshake: '🤝', v: '✌️', crossed_fingers: '🤞', eyes: '👀',
  see_no_evil: '🙈', hear_no_evil: '🙉', speak_no_evil: '🙊',
  // 心/符号
  heart: '❤️', broken_heart: '💔', two_hearts: '💕', sparkling_heart: '💖',
  yellow_heart: '💛', blue_heart: '💙', green_heart: '💚', purple_heart: '💜',
  star: '⭐', star2: '🌟', sparkles: '✨', zap: '⚡', fire: '🔥', boom: '💥',
  '100': '💯', warning: '⚠️', question: '❓', exclamation: '❗',
  white_check_mark: '✅', heavy_check_mark: '✔️', x: '❌',
  no_entry: '⛔', no_entry_sign: '🚫', red_circle: '🔴', green_circle: '🟢',
  yellow_circle: '🟡', arrow_right: '➡️', arrow_left: '⬅️', arrow_up: '⬆️',
  arrow_down: '⬇️', zzz: '💤', ok: '🆗', new: '🆕', sos: '🆘',
  // 庆祝/物品
  tada: '🎉', confetti_ball: '🎊', balloon: '🎈', gift: '🎁', trophy: '🏆',
  medal_sports: '🏅', crown: '👑', gem: '💎', rocket: '🚀', airplane: '✈️',
  car: '🚗', bike: '🚲', hourglass: '⌛', watch: '⌚', alarm_clock: '⏰',
  calendar: '📅', memo: '📝', pencil2: '✏️', book: '📖', books: '📚',
  bookmark: '🔖', link: '🔗', paperclip: '📎', scissors: '✂️',
  lock: '🔒', unlock: '🔓', key: '🔑', bell: '🔔', mag: '🔍', bulb: '💡',
  moneybag: '💰', dollar: '💵', chart_with_upwards_trend: '📈',
  chart_with_downwards_trend: '📉', bar_chart: '📊', clipboard: '📋',
  pushpin: '📌', file_folder: '📁', open_file_folder: '📂',
  page_facing_up: '📄', email: '📧', envelope: '✉️', phone: '📱',
  telephone: '☎️', computer: '💻', keyboard: '⌨️', desktop_computer: '🖥️',
  printer: '🖨️', camera: '📷', video_camera: '📹', tv: '📺', speaker: '🔊',
  mute: '🔇', loudspeaker: '📢', hammer: '🔨', wrench: '🔧', gear: '⚙️',
  bug: '🐛', ant: '🐜', bee: '🐝', spider: '🕷️',
  // 自然/食物
  sunny: '☀️', cloud: '☁️', umbrella: '☔', snowflake: '❄️', rainbow: '🌈',
  moon: '🌙', earth_asia: '🌏', seedling: '🌱', four_leaf_clover: '🍀',
  rose: '🌹', coffee: '☕', tea: '🍵', beer: '🍺', pizza: '🍕',
  apple: '🍎', banana: '🍌', cake: '🍰', birthday: '🎂',
  dog: '🐶', cat: '🐱', mouse: '🐭', rabbit: '🐰', bear: '🐻',
  panda_face: '🐼', penguin: '🐧', bird: '🐦', fish: '🐟', turtle: '🐢',
};

const SHORTCODE_RE = /:([a-z0-9_+-]+):/g;

// 递归替换 mdast 文本节点里的 :shortcode:（跳过代码类节点）
function transformNode(node) {
  if (node.type === 'code' || node.type === 'inlineCode' ||
      node.type === 'math' || node.type === 'inlineMath' || node.type === 'yaml') {
    return;
  }
  if (node.type === 'text' && typeof node.value === 'string') {
    node.value = node.value.replace(SHORTCODE_RE, (m, name) => EMOJI[name] || m);
    return;
  }
  if (Array.isArray(node.children)) {
    for (const child of node.children) transformNode(child);
  }
}

// remark 插件：解析后把短代码转成 Unicode 字符（序列化时保持 Unicode，
// 与 remark-emoji 行为一致）
export default function remarkEmojiLite() {
  return (tree) => {
    transformNode(tree);
    return tree;
  };
}

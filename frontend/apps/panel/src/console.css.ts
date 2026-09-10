import { tokens } from '@ark/ui'

/**
 * 问答控制台用到的少量全局 CSS（hover / 伪元素 / keyframes / 移动端抽屉）。
 *
 * 纪律（对齐 ark-iam DESIGN §7.1）：css 文件不新增，页面内 <style> 不散落；
 * 只在应用壳（app.tsx）注入一次。CSS-in-JS 无法表达的交互态（:hover / ::before /
 * @keyframes / @media）统一收口在这里。
 */
export const consoleStyleSheet = `
  html, body, #root { height: 100%; }
  body {
    margin: 0;
    background: ${tokens.bg};
    color: ${tokens.text};
    font-family: ${tokens.fontFamily};
    -webkit-font-smoothing: antialiased;
    text-rendering: optimizeLegibility;
  }
  @keyframes fg-blink {
    0%, 100% { opacity: 1; }
    50% { opacity: 0; }
  }
  @keyframes fg-pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.28; }
  }
  .fg-anim-blink { animation: fg-blink 1.1s ease-in-out infinite; }
  .fg-anim-pulse { animation: fg-pulse 1.4s ease-in-out infinite; }

  /* 会话列表项：hover / 选中（::before 选中竖条） */
  .fg-sess-item {
    position: relative;
    transition: background 0.12s ease;
  }
  .fg-sess-item:hover { background: ${tokens.softFill}; }
  .fg-sess-item.fg-on {
    background: ${tokens.selectedBg};
  }
  .fg-sess-item.fg-on::before {
    content: '';
    position: absolute;
    left: 0;
    top: 9px;
    bottom: 9px;
    width: 2.5px;
    border-radius: 2px;
    background: ${tokens.primary};
  }
  .fg-sess-item:disabled { opacity: 0.6; cursor: not-allowed; }
  .fg-sess-item:disabled:hover { background: transparent; }

  /* 新建会话：agent 选项卡（基础边框/底色由类承载，hover/选中才能覆盖） */
  .fg-agent-opt {
    border: 1px solid ${tokens.border};
    border-radius: 13px;
    background: ${tokens.surface};
    transition: box-shadow 0.12s ease, border-color 0.12s ease, background 0.12s ease;
    cursor: pointer;
  }
  .fg-agent-opt:hover { box-shadow: inset 0 0 0 1px ${tokens.borderStrong}; }
  .fg-agent-opt.fg-sel {
    border-color: ${tokens.primary};
    background: ${tokens.primarySoft};
    box-shadow: inset 0 0 0 1px ${tokens.primary};
  }

  /* 会话侧栏（桌面静态分栏；≤920px 在下方媒体里抽屉化） */
  .fg-rail {
    width: 288px;
    flex: none;
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: ${tokens.surface};
    border-right: 1px solid ${tokens.border};
  }

  /* 移动端（≤920px）：burger 出现、侧栏抽屉化、管理入口收纳 */
  .fg-burger { display: none; }
  .fg-scrim-mobile { display: none; }
  @media (max-width: 920px) {
    .fg-burger { display: inline-flex; }
    .fg-nav { display: none; }
    .fg-rail {
      position: fixed;
      top: 52px;
      bottom: 0;
      left: 0;
      z-index: 40;
      width: min(300px, 84vw);
      box-shadow: 0 0 60px -24px rgba(15, 23, 42, 0.45);
      transform: translateX(-104%);
      transition: transform 0.18s ease;
    }
    .fg-rail.fg-open { transform: translateX(0); }
    .fg-scrim-mobile {
      display: block;
    }
  }

  /* 输入区：border/圆角由类承载，便于 :focus-within 覆盖 */
  .fg-composer {
    border: 1px solid ${tokens.border};
    border-radius: 16px;
    background: ${tokens.surface};
    transition: border-color 0.15s ease, box-shadow 0.15s ease;
  }
  .fg-composer:focus-within {
    border-color: ${tokens.borderStrong};
    box-shadow: 0 0 0 3px ${tokens.primarySoft};
  }

  /* 输入区底栏的按键提示（可点击切档）：单行 + 省略号 + 可裁剪。
     窄容器里逐词换行会把底栏行高撑开，溢出的文字又被后绘制的模型选择器白底盖住，
     看起来就是「文字重影」——所以这里显式禁止换行。 */
  .fg-hint-kbd {
    max-width: 100%;
    border: none;
    background: transparent;
    padding: 0;
    margin: 0;
    cursor: pointer;
    font-family: inherit;
    font-size: inherit;
    color: inherit;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .fg-hint-kbd:hover { text-decoration: underline; }

  /* 模型选择器关闭态：药丸（不落表单框感）——hairline 边 + 全圆角 + mono 小字，
     与同一行的「在项目中工作」弱字、圆形发送钮同一语言；hover 抬边+软底，聚焦出靛蓝环。 */
  .fg-model-pill {
    width: auto;
    min-width: 132px;
    max-width: 240px;
    height: 28px;
    padding: 0 10px;
    gap: 6px;
    border: 1px solid ${tokens.border};
    border-radius: 999px;
    background: ${tokens.surface};
    cursor: pointer;
    transition: border-color 0.12s ease, background 0.12s ease, box-shadow 0.12s ease, min-width 0.12s ease;
  }
  .fg-model-pill:hover {
    border-color: ${tokens.borderStrong};
    background: ${tokens.softFill};
  }
  /* 聚焦/展开：静息态贴合文案（不长出一片空白），一旦交互就展开到可读可搜的宽度。 */
  .fg-model-pill:focus-within {
    min-width: 216px;
    border-color: ${tokens.primary};
    box-shadow: 0 0 0 3px ${tokens.primarySoft};
  }
  /* 清除按钮（allowClear 时的 ⊗）：antd 默认是 25% 黑，统一到三级灰阶。 */
  .fg-model-pill .ant-select-clear {
    color: ${tokens.textPlaceholder};
    font-size: 12px;
  }
  .fg-model-pill .ant-select-clear:hover { color: ${tokens.textSecondary}; }

  /* 模型下拉浮层：浮层才可以有阴影（与 app.tsx toast 同一条投影），列表项圆角 + 选中软底。 */
  .fg-model-pop {
    border: 1px solid ${tokens.border};
    box-shadow: 0 8px 30px rgba(15, 23, 42, 0.14);
  }
  /* 双类选择器：压过 antd 生成的分组内边距/圆角（单类会被 antd 规则盖掉）。 */
  .fg-model-pop .ant-select-item-option.fg-model-pop-item {
    border-radius: 8px;
    padding: 5px 10px 5px 12px;
  }
  /* 分组标题（运行时下发的 group_name）：mono 小字弱化，别和模型名抢视线。 */
  .fg-model-pop .ant-select-item-group {
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    color: ${tokens.textPlaceholder};
  }

  /* 会话行「更多」按钮：悬停浮现；触屏（移动抽屉态）常显 */
  .fg-sess-more { opacity: 0; transition: opacity 0.12s ease; }
  .fg-sess-item:hover .fg-sess-more,
  .fg-sess-more.fg-keep { opacity: 1; }
  @media (max-width: 920px) {
    .fg-sess-more { opacity: 1; }
  }
`

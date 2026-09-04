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
`

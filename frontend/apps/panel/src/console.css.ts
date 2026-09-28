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

  /* 斜杠命令候选弹层（ADR-17 命令发现面）：贴在输入框上方，形态对齐模型下拉的浮层
     （hairline 边 + 同一档阴影），但它是 composer 内的普通文档流，不用绝对定位——
     弹层出现时输入框整体上移，比盖住正文更不容易看错当前在打什么。 */
  .fg-cmd-pop {
    display: flex;
    flex-direction: column;
    padding: 4px;
    border: 1px solid ${tokens.border};
    border-radius: 10px;
    background: ${tokens.surface};
    box-shadow: 0 8px 30px rgba(15, 23, 42, 0.14);
  }
  /* 只有候选列表滚动，页脚（"命令由底座提供，平台只做回显"）常驻可见——
     候选多时（opencode 实测 23 条）它是用户判断"这些命令从哪来"的唯一线索。 */
  .fg-cmd-list {
    display: flex;
    flex-direction: column;
    gap: 1px;
    max-height: 212px;
    overflow-y: auto;
  }
  .fg-cmd-item {
    display: flex;
    align-items: baseline;
    gap: 8px;
    width: 100%;
    padding: 5px 8px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    text-align: left;
    cursor: pointer;
    font: inherit;
    color: ${tokens.text};
  }
  /* 键盘高亮与鼠标悬停共用一个态：两边都能看出"Enter 会填入哪一条"。 */
  .fg-cmd-item.fg-on { background: ${tokens.softFill}; }
  .fg-cmd-name {
    flex: none;
    font-family: ${tokens.fontMono};
    font-size: 12.5px;
    color: ${tokens.text};
  }
  .fg-cmd-hint {
    flex: none;
    font-family: ${tokens.fontMono};
    font-size: 11px;
    color: ${tokens.textPlaceholder};
  }
  .fg-cmd-desc {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 11.5px;
    color: ${tokens.textSecondary};
  }
  /* 页脚：把"命令由底座提供、平台只回显"写在用户看得见的地方——
     免得在 dsh 上（它不通告命令）把"没有候选"误读成"平台没做这个功能"。 */
  .fg-cmd-foot {
    padding: 3px 8px 1px;
    font-size: 10.5px;
    color: ${tokens.textPlaceholder};
  }

  /* 工作空间药丸（goose DirSwitcher 的触发按钮形态）：图标 + 目录名，完整路径走 tooltip；
     与同一行的模型药丸共享一套静息形态（hairline 边 + 全圆角 + 小字）。 */
  .fg-ws-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 190px;
    height: 28px;
    padding: 0 10px;
    border: 1px solid ${tokens.border};
    border-radius: 999px;
    background: ${tokens.surface};
    cursor: pointer;
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    color: ${tokens.text};
    transition: border-color 0.12s ease, background 0.12s ease;
  }
  .fg-ws-pill:hover:not(:disabled) {
    border-color: ${tokens.borderStrong};
    background: ${tokens.softFill};
  }
  .fg-ws-pill:disabled { cursor: default; opacity: 0.55; }
  .fg-ws-pill-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* 工作空间菜单：小节标题 / 分隔线 / 两行目录项（goose 的 DropdownMenuLabel + DirNameLabel）。 */
  /* 会话能力选择器（新建会话时勾选技能/MCP 子集；壳沿用工作空间弹层的语言）。 */
  .fg-cap-pop { display: flex; flex-direction: column; width: 380px; max-height: 460px; overflow-y: auto; }
  .fg-cap-loading {
    display: flex; align-items: center; gap: 8px;
    padding: 10px 8px; font-size: 12px; color: ${tokens.textSecondary};
  }
  .fg-cap-head { display: flex; align-items: center; justify-content: space-between; gap: 6px; }
  /* 跟随默认 / 自定义：两态必须一眼看出当前在哪一态（它决定请求体里那个键在不在）。 */
  .fg-cap-mode { display: inline-flex; gap: 2px; padding: 0 6px 2px; }
  .fg-cap-mode-btn {
    padding: 2px 8px;
    border: 1px solid transparent;
    border-radius: 999px;
    background: transparent;
    cursor: pointer;
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    color: ${tokens.textSecondary};
  }
  .fg-cap-mode-btn:hover { color: ${tokens.text}; }
  .fg-cap-mode-btn.fg-on {
    border-color: ${tokens.border};
    background: ${tokens.softFill};
    color: ${tokens.text};
  }
  .fg-cap-note {
    display: flex; align-items: center; justify-content: space-between; gap: 6px;
    padding: 2px 8px 4px;
    font-size: 11px; line-height: 1.5; color: ${tokens.textPlaceholder};
  }
  .fg-cap-mini {
    border: none; background: transparent; cursor: pointer; padding: 0 4px;
    font-family: ${tokens.fontMono}; font-size: 10.5px; color: ${tokens.primary};
  }
  .fg-cap-mini:hover { text-decoration: underline; }
  .fg-cap-list { display: flex; flex-direction: column; max-height: 190px; overflow-y: auto; }
  .fg-cap-opt {
    display: flex; align-items: center; justify-content: space-between; gap: 6px;
    padding: 4px 8px; border-radius: 8px; transition: background 0.12s ease;
  }
  .fg-cap-opt:hover { background: ${tokens.softFill}; }
  /* 不可选（服务端也会拒）：明确压暗，原因在右侧标签的 tooltip 里逐条给出。 */
  .fg-cap-opt.fg-off { opacity: 0.6; }
  .fg-cap-opt.fg-off:hover { background: transparent; }
  .fg-cap-opt .ant-checkbox-wrapper { min-width: 0; display: flex; align-items: center; }
  .fg-cap-opt-name {
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    font-size: 12.5px; color: ${tokens.text};
  }
  .fg-cap-opt-id {
    margin-left: 6px; font-family: ${tokens.fontMono}; font-size: 10.5px; color: ${tokens.textPlaceholder};
  }
  .fg-cap-opt-why {
    flex: none; cursor: help;
    font-family: ${tokens.fontMono}; font-size: 10px; color: ${tokens.textPlaceholder};
    border-bottom: 1px dotted ${tokens.borderStrong};
  }
  /* 已限制（自定义）的能力药丸：与"跟随默认"必须看得出区别。 */
  .fg-ws-pill.fg-cap-on {
    border-color: ${tokens.primary};
    color: ${tokens.primary};
  }

  .fg-ws-pop { display: flex; flex-direction: column; }
  .fg-ws-label {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 6px;
    padding: 4px 8px 2px;
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    color: ${tokens.textPlaceholder};
  }
  .fg-ws-sep {
    height: 1px;
    margin: 4px 2px;
    background: ${tokens.border};
  }
  .fg-ws-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 8px;
    border: none;
    border-radius: 8px;
    background: transparent;
    cursor: pointer;
    text-align: left;
    font-family: inherit;
    color: ${tokens.text};
    transition: background 0.12s ease;
  }
  .fg-ws-item:hover:not(:disabled) { background: ${tokens.softFill}; }
  /* 当前目录行：信息展示不是动作（平台上没有"在文件管理器中打开"这类能力），
     所以不给 hover 反馈，免得看着像能点。 */
  .fg-ws-item-static { cursor: default; }
  .fg-ws-item-static:hover { background: transparent; }
  .fg-ws-item:disabled { cursor: default; opacity: 0.5; }
  .fg-ws-item-icon { flex: none; font-size: 13px; color: ${tokens.textPlaceholder}; }
  .fg-ws-item-label { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .fg-ws-item-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12.5px;
    line-height: 1.35;
  }
  .fg-ws-item-sub {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    line-height: 1.35;
    color: ${tokens.textPlaceholder};
  }
  .fg-ws-item-meta {
    flex: none;
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    color: ${tokens.textPlaceholder};
  }
  .fg-ws-item-check { flex: none; color: ${tokens.text}; font-size: 12px; }

  /* 浏览视图头：返回 / 上级 / 主目录 + 当前待选路径（尾段优先，长路径截前缀）。 */
  .fg-ws-browse-head { display: flex; align-items: center; gap: 2px; }
  .fg-ws-icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    flex: none;
    border: none;
    border-radius: 6px;
    background: transparent;
    cursor: pointer;
    font-size: 12px;
    color: ${tokens.textSecondary};
    transition: background 0.12s ease, color 0.12s ease;
  }
  .fg-ws-icon-btn:hover:not(:disabled) { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-ws-icon-btn:disabled { cursor: default; opacity: 0.35; }
  .fg-ws-cur {
    padding: 0 8px 2px;
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    color: ${tokens.textSecondary};
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    direction: rtl; /* 路径过长时保留尾段（"我在哪个目录"比"从哪来"重要） */
    text-align: left;
  }
  .fg-ws-empty {
    padding: 4px 8px;
    font-size: 11.5px;
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

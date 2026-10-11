import { tokens } from '@ark-mate/ui'

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

  /* 左栏（导航 + 会话区 + 状态）：桌面常驻 288px；≤920px 在下方媒体里抽屉化 */
  .fg-rail {
    width: 288px;
    flex: none;
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: ${tokens.surface};
    border-right: 1px solid ${tokens.border};
  }

  /* 品牌区：48px（原顶栏 52px，省下的 4px 留给会话区） */
  .fg-rail-brand {
    flex: none;
    display: flex;
    align-items: center;
    height: 48px;
    padding: 0 14px;
    font-size: 15px;
    font-weight: 660;
    letter-spacing: -0.02em;
    color: ${tokens.text};
    text-decoration: none;
    white-space: nowrap;
  }

  /* 导航区：一级 32px / 二级 30px，条目间距 1px */
  .fg-nav-rail {
    flex: none;
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 0 8px 8px;
  }
  .fg-navitem {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 32px;
    padding: 0 10px;
    /* 导航项里既有 <NavLink>（a）也有 <button>（组行文字区 / footer 触发器）：
       - 不重置背景，button 会带 UA 的 buttonface 底（#efefef），底部多出一块灰"区块"；
       - 不重置 text-align，button 的 UA text-align:center 会把 flex:1 的 label 文字顶到该行中间，
         图标与文字之间就出现一条大缝（2026-10-10 实测踩到）。 */
    border: 0;
    background: transparent;
    font: inherit;
    text-align: left;
    border-radius: 8px;
    font-size: 13px;
    color: ${tokens.textSecondary};
    text-decoration: none;
    cursor: pointer;
    user-select: none;
    transition: background 0.12s ease, color 0.12s ease;
  }
  .fg-navitem:hover { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-navitem.fg-on { background: ${tokens.softFill}; color: ${tokens.text}; font-weight: 600; }
  /* 一级项图标：定宽 16 让所有文案左对齐；未选中比文案再弱一档，选中吃主色 */
  .fg-navitem-icon {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    font-size: 14.5px;
    color: ${tokens.textPlaceholder};
  }
  .fg-navitem:hover .fg-navitem-icon { color: ${tokens.textSecondary}; }
  .fg-navitem.fg-on .fg-navitem-icon { color: ${tokens.primary}; }
  .fg-navitem-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fg-navitem-count { flex: none; font-family: ${tokens.fontMono}; font-size: 11px; color: ${tokens.textSecondary}; }

  /* 组行：文字区（进总览 + 展开）与箭头区分工，箭头的点击面独立 */
  .fg-navgroup-row { padding-right: 4px; }
  .fg-navgroup-text {
    flex: 1;
    min-width: 0;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 32px;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .fg-navgroup-caret {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: ${tokens.textPlaceholder};
    cursor: pointer;
  }
  .fg-navgroup-caret:hover { background: ${tokens.border}; color: ${tokens.text}; }

  /* 二级项：缩进 26px；命中态用主色文字 + 弱底 */
  .fg-navsub-list { display: flex; flex-direction: column; gap: 1px; }
  .fg-navsub {
    display: flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 10px 0 26px;
    border-radius: 8px;
    font-size: 12.5px;
    color: ${tokens.textSecondary};
    text-decoration: none;
    cursor: pointer;
    transition: background 0.12s ease, color 0.12s ease;
  }
  .fg-navsub:hover { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-navsub.fg-on { background: ${tokens.softFill}; color: ${tokens.primary}; font-weight: 600; }
  .fg-navsub-label { flex: 1; min-width: 0; }
  /* pending（待开发）：可读但不可点，右挂「待开发」标签 */
  .fg-navsub.fg-pending { color: ${tokens.textPlaceholder}; cursor: default; }
  .fg-navsub.fg-pending:hover { background: transparent; color: ${tokens.textPlaceholder}; }
  .fg-navsub-tag {
    flex: none;
    padding: 0 5px;
    border: 1px solid ${tokens.border};
    border-radius: 4px;
    font-size: 10px;
    line-height: 15px;
    color: ${tokens.textPlaceholder};
  }

  /* 会话区：占满品牌/导航/footer 之外的高度（滚动落在内部列表上）；上面一道 hairline 把「导航」与「会话」两段分开 */
  .fg-rail-pane {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    border-top: 1px solid ${tokens.border};
  }

  /* 左栏 footer：固定左下，点「设置」在**它上方**展开二级菜单（向上生长）。这一行只有「设置」一个入口
     （令牌已收进设置菜单；WS 状态在窗口右下角的全局状态条，不重复一份）。
     高度是 auto：菜单展开时 footer 变高、会话区（flex:1）相应让位，不需要浮层/portal。 */
  .fg-rail-foot {
    flex: none;
    /* 兜底：会话区恒常驻（flex:1 已吃掉 free space），这里不产生位移；
       万一某路由不渲染会话区，auto margin 仍会把 footer 顶到左栏底。 */
    margin-top: auto;
    display: flex;
    flex-direction: column;
    border-top: 1px solid ${tokens.border};
  }
  .fg-rail-footrow {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    height: 40px;
    /* 与 .fg-nav-rail 同为 8px：设置图标与主导航图标同一条竖线 */
    padding: 0 8px;
  }
  .fg-rail-footrow .fg-navitem { flex: 1; min-width: 0; }

  /* 设置弹窗（一体式）：左列分节列表 + 右列内容，各自滚动；换节时弹窗高矮不变。 */
  .fg-setdlg {
    display: flex;
    height: min(64vh, 640px);
  }
  .fg-setdlg-nav {
    flex: none;
    width: 208px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding-right: 12px;
    border-right: 1px solid ${tokens.border};
    overflow-y: auto;
  }
  .fg-setdlg-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border: 0;
    border-radius: 10px;
    background: transparent;
    font: inherit;
    text-align: left;
    cursor: pointer;
    color: ${tokens.textSecondary};
    transition: background 0.12s ease, color 0.12s ease;
  }
  .fg-setdlg-item:hover { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-setdlg-item.fg-on { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-setdlg-icon {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    font-size: 15px;
    color: ${tokens.textPlaceholder};
  }
  .fg-setdlg-item:hover .fg-setdlg-icon { color: ${tokens.textSecondary}; }
  .fg-setdlg-item.fg-on .fg-setdlg-icon { color: ${tokens.primary}; }
  .fg-setdlg-text { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .fg-setdlg-label { font-size: 13px; line-height: 1.4; }
  .fg-setdlg-item.fg-on .fg-setdlg-label { font-weight: 600; }
  .fg-setdlg-hint { font-size: 11px; line-height: 1.4; color: ${tokens.textPlaceholder}; }
  .fg-setdlg-body { flex: 1; min-width: 0; padding-left: 20px; overflow-y: auto; }

  /* ——— 设置 · 模型（第 4 节）：两个平级的块（供应商 / 模型） ———
     颜色/圆角/间距一律取 tokens（业务代码与样式都不得硬编码色值）。 */
  .fg-model-panel { display: flex; flex-direction: column; gap: 16px; }
  .fg-model-panel .fg-card { border: 1px solid ${tokens.border}; border-radius: ${tokens.radiusMd}; background: ${tokens.surface}; overflow: hidden; }
  .fg-model-panel .fg-card-body { padding: 12px 16px; display: flex; flex-direction: column; gap: 8px; }

  /* 配置状态条：路径 / 生成时间 / 通道 / pending。pending 必须看得见——它与"已保存"是两件事。 */
  .fg-cfgbar {
    display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
    padding: 6px 10px; border-radius: ${tokens.radiusSm};
    background: ${tokens.softFill}; font-size: 12px; color: ${tokens.textSecondary};
  }
  .fg-cfgbar-dot { width: 6px; height: 6px; border-radius: 3px; background: ${tokens.success}; flex: none; }
  .fg-cfgbar-dot.fg-pending { background: ${tokens.warning}; }
  .fg-cfgbar-dim { color: ${tokens.textPlaceholder}; font-size: 11.5px; }

  /* 生效说明（固定一行）：把"默认"与"会话内另选"的两层关系说清，避免用户以为默认=强制。 */
  .fg-diffnote { font-size: 12px; line-height: 1.6; color: ${tokens.textPlaceholder}; }
  .fg-diffnote-inline { margin-left: 6px; color: ${tokens.primary}; cursor: help; }

  /* 差集列表：逐条给出 model_id + 严重度 + 一句可执行的说明。 */
  .fg-difflist { margin: 0; padding-left: 16px; font-size: 12px; line-height: 1.7; }
  .fg-difflist li { display: flex; gap: 8px; align-items: baseline; }
  .fg-sev { flex: none; font-size: 11px; padding: 0 6px; border-radius: ${tokens.radiusSm}; }
  .fg-sev.fg-error { background: ${tokens.primarySoft}; color: ${tokens.error}; }
  .fg-sev.fg-degrade { background: ${tokens.warningBg}; color: ${tokens.warning}; }
  .fg-sev.fg-info { background: ${tokens.softFill}; color: ${tokens.textPlaceholder}; }

  /* 「底座独有」折叠区：默认非空（opencode 的内置目录），所以它不该长得像报错。 */
  .fg-advertised { border-top: 1px dashed ${tokens.border}; padding-top: 8px; }
  .fg-advertised-head { font-size: 12px; color: ${tokens.textSecondary}; display: flex; align-items: center; }
  .fg-advertised-list { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 6px; max-height: 96px; overflow-y: auto; }
  .fg-advertised-item {
    display: inline-flex; align-items: center; gap: 6px; font-size: 11.5px;
    padding: 1px 6px; border: 1px solid ${tokens.border}; border-radius: ${tokens.radiusSm};
    color: ${tokens.textSecondary};
  }

  /* 自检结果：就地展开成 check 列表（pass 绿 / fail 红 / warn·unknown 灰），每条带 remedy。 */
  .fg-probe { margin-top: 8px; border: 1px solid ${tokens.border}; border-radius: ${tokens.radiusSm}; overflow: hidden; }
  .fg-probe-head { display: flex; align-items: center; gap: 8px; padding: 6px 10px; background: ${tokens.softFill}; font-size: 12.5px; }
  .fg-probe-row { display: flex; align-items: baseline; gap: 8px; padding: 5px 10px; border-top: 1px solid ${tokens.border}; font-size: 12px; }
  .fg-probe-dot { width: 6px; height: 6px; border-radius: 3px; background: ${tokens.textPlaceholder}; flex: none; }
  .fg-probe-dot.fg-pass { background: ${tokens.success}; }
  .fg-probe-dot.fg-fail { background: ${tokens.error}; }
  .fg-probe-name { flex: none; width: 76px; color: ${tokens.textSecondary}; font-family: ${tokens.fontMono}; font-size: 11.5px; }
  .fg-probe-detail { flex: 1; min-width: 0; }
  .fg-probe-remedy { flex: none; max-width: 46%; color: ${tokens.primary}; }

  /* ——— 设置 · 模型（2026-10-11 交互改造）：行卡 + 就地展开的编辑卡 ———
     语汇参照 deepseek-harness 的「自定义模型 API」卡片：表格换成行卡、弹窗换成原位展开，
     依赖的仍是上面那几组既有类（.fg-card / .fg-cfgbar / .fg-probe / .fg-diffnote）。 */
  .fg-model-panel .fg-card-body { gap: 10px; }

  /* 保存成功（role=status）：只说「已保存」，绝不说「已生效」。 */
  .fg-savedline {
    margin: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    color: #15803d;
  }
  .fg-savedline::before { content: '✓'; font-size: 12px; }

  /* 行卡列表：身份 + 状态点在左，动作在右；不再是"每列一个字段"的表格。 */
  .fg-rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .fg-rowcard {
    border: 1px solid ${tokens.border};
    border-radius: ${tokens.radiusMd};
    background: ${tokens.surface};
    padding: 9px 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .fg-rowhead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
  .fg-rowname { font-size: 13.5px; font-weight: 600; }
  .fg-rowid { font-family: ${tokens.fontMono}; font-size: 11px; color: ${tokens.textPlaceholder}; }
  .fg-modelid { font-family: ${tokens.fontMono}; font-size: 12.5px; }
  .fg-rowmeta { font-size: 11.5px; color: ${tokens.textPlaceholder}; }
  .fg-rowtag {
    flex: none;
    display: inline-flex;
    align-items: center;
    height: 19px;
    padding: 0 6px;
    border: 1px solid ${tokens.border};
    border-radius: 6px;
    background: transparent;
    font: inherit;
    font-size: 11px;
    color: ${tokens.textSecondary};
  }
  .fg-rowtag.fg-warn { border-color: ${tokens.warningBorder}; background: ${tokens.warningBg}; color: #b45309; }
  .fg-rowtag.fg-info { border-color: ${tokens.primary}; background: ${tokens.primarySoft}; color: ${tokens.primary}; }
  .fg-rowtag.fg-clickable { cursor: pointer; }
  .fg-rowtag.fg-clickable:hover { border-color: ${tokens.primary}; color: ${tokens.primary}; }
  .fg-rowactions { display: inline-flex; align-items: center; gap: 2px; margin-left: auto; }
  .fg-rowproblems { margin: 0; font-size: 11.5px; line-height: 1.5; color: ${tokens.warning}; }

  /* 密钥状态点：绿 = 已配置、红 = 缺失。绝不回显密钥本身。 */
  .fg-keydot { display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; }
  .fg-keydot.fg-key-on { background: ${tokens.success}; }
  .fg-keydot.fg-key-off { background: ${tokens.error}; }

  /* 就地展开的编辑卡：凹进面板的模块底（与白卡分层），字段竖排、底部右对齐动作。 */
  .fg-editor {
    border-radius: ${tokens.radiusMd};
    background: ${tokens.softFill};
    padding: 12px 14px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .fg-editor-head { display: flex; align-items: baseline; gap: 8px; }
  .fg-editor-title { font-size: 13.5px; font-weight: 600; }
  .fg-editor-route { font-family: ${tokens.fontMono}; font-size: 11.5px; color: ${tokens.textPlaceholder}; }
  .fg-editor-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 14px; }
  .fg-editor-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; padding-top: 2px; }
  .fg-noopline { margin: 0 auto 0 0; font-size: 12px; color: ${tokens.textSecondary}; }
  .fg-hint { margin: 4px 0 0; font-size: 11.5px; line-height: 1.5; color: ${tokens.textPlaceholder}; }

  /* 编辑卡里的表单（antd）：标签 12px、提示贴在字段下方——参照稿的 .field 语汇。 */
  .fg-model-panel .ant-form-item { margin-bottom: 10px; }
  .fg-model-panel .ant-form-item-label { padding-bottom: 2px; }
  .fg-model-panel .ant-form-item-label > label { height: auto; font-size: 12px; color: ${tokens.textSecondary}; }
  .fg-model-panel .ant-form-item-extra {
    min-height: 0;
    padding-top: 3px;
    font-size: 11.5px;
    line-height: 1.5;
    color: ${tokens.textPlaceholder};
  }
  .fg-model-panel .ant-form-item-explain-error { font-size: 11.5px; }

  /* 「新建」是列表的最后一格：虚线整宽，读起来像"一个位置"而不是"一条命令"。 */
  .fg-addbtn {
    width: 100%;
    height: 42px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    border: 1px dashed ${tokens.borderStrong};
    border-radius: ${tokens.radiusMd};
    background: transparent;
    color: ${tokens.text};
    font: inherit;
    font-size: 13.5px;
    cursor: pointer;
    transition: background 0.12s ease, border-color 0.12s ease, color 0.12s ease;
  }
  .fg-addbtn:hover { background: ${tokens.softFill}; border-color: ${tokens.primary}; color: ${tokens.primary}; }

  /* 分组头（模型块按供应商分组）：空组本身就是要说的事。 */
  .fg-group { display: flex; flex-direction: column; gap: 8px; }
  .fg-grouphead {
    display: flex;
    align-items: center;
    gap: 8px;
    padding-top: 8px;
    border-top: 1px solid ${tokens.border};
  }
  .fg-groupname { font-size: 12.5px; font-weight: 600; }
  .fg-linkbtn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 8px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    color: ${tokens.primary};
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  .fg-linkbtn:hover:not(:disabled) { background: ${tokens.primarySoft}; }
  .fg-linkbtn:disabled { opacity: 0.45; cursor: default; }

  /* 空态：虚线框，并且说清"空 = 什么后果 / 下一步干什么"。 */
  .fg-emptycard {
    padding: 12px 14px;
    border: 1px dashed ${tokens.borderStrong};
    border-radius: ${tokens.radiusMd};
    font-size: 12px;
    line-height: 1.6;
    color: ${tokens.textPlaceholder};
  }
  .fg-emptycard b { display: block; margin-bottom: 2px; font-size: 13px; font-weight: 600; color: ${tokens.textSecondary}; }
  .fg-emptyline {
    margin: 0;
    padding: 9px 12px;
    border: 1px dashed ${tokens.border};
    border-radius: ${tokens.radiusSm};
    font-size: 12px;
    color: ${tokens.textPlaceholder};
    text-align: center;
  }

  @media (max-width: 760px) {
    .fg-editor-grid { grid-template-columns: minmax(0, 1fr); }
  }

  /* 删除确认弹窗：只放一段"会连带发生什么"的说明，动作只有取消 / 删除。 */
  .fg-confirm .ant-modal-body { font-size: 12.5px; line-height: 1.65; }

  /* 设置弹窗的内容体（每节共用）：顶部一句说明 + 右侧动作，下面一张白卡。 */
  .fg-setsec { display: flex; flex-direction: column; gap: 12px; }
  .fg-setsec-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
  }
  .fg-setsec-desc { font-size: 12.5px; line-height: 1.6; color: ${tokens.textSecondary}; }
  .fg-setsec-card {
    border: 1px solid ${tokens.border};
    border-radius: ${tokens.radiusMd};
    background: ${tokens.surface};
    overflow: hidden;
  }

  .fg-visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }

  /* 窄屏顶条（宽屏隐藏）：左栏收成抽屉后唯一的开启入口 */
  .fg-burger {
    display: none;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    flex: none;
    border: 1px solid ${tokens.border};
    border-radius: 8px;
    background: ${tokens.surface};
    color: ${tokens.text};
    cursor: pointer;
  }
  .fg-mobilebar { display: none; }
  .fg-scrim-mobile { display: none; }

  /* 移动端（≤920px）：左栏抽屉化（top 0——顶栏已撤，抽屉盖满全高） */
  @media (max-width: 920px) {
    /* 窄屏设置弹窗：分节列表横过来当顶条，内容占满（hint 会折行把列表撑高，隐掉） */
    .fg-setdlg { flex-direction: column; height: min(68vh, 560px); }
    .fg-setdlg-nav {
      width: auto;
      flex-direction: row;
      gap: 6px;
      padding: 0 0 10px;
      border-right: 0;
      border-bottom: 1px solid ${tokens.border};
      overflow-x: auto;
      overflow-y: hidden;
    }
    .fg-setdlg-item { flex: none; }
    .fg-setdlg-hint { display: none; }
    .fg-setdlg-body { padding: 12px 0 0; }

    .fg-burger { display: inline-flex; }
    .fg-mobilebar {
      display: flex;
      align-items: center;
      gap: 10px;
      flex: none;
      height: 52px;
      padding: 0 12px;
      background: ${tokens.surface};
      border-bottom: 1px solid ${tokens.border};
    }
    .fg-mobilebar-title { font-size: 13.5px; font-weight: 600; color: ${tokens.text}; }
    .fg-rail {
      position: fixed;
      top: 0;
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

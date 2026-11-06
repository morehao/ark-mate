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

  /* 会话列表项：hover / 选中（设计 §16：选中仅浅灰底，去掉主色左竖条，标题不加粗不改色） */
  .fg-sess-item {
    position: relative;
    transition: background 0.12s ease;
  }
  .fg-sess-item:hover { background: ${tokens.softFill}; }
  .fg-sess-item.fg-on {
    background: ${tokens.softFill};
  }
  .fg-sess-item:disabled { opacity: 0.6; cursor: not-allowed; }
  .fg-sess-item:disabled:hover { background: transparent; }

  /* v19：这里原有「新建会话：agent 选项卡」的三条 fg-agent-opt 规则。
     选项卡本身早已隐去（新建固定用当前 agent），agent 概念删除后它更不可能回来——
     留着一份没有渲染点的样式，下一个人只会去猜"哪还在用它"。
     （本文件是模板字符串，注释里不能出现反引号——那会直接闭合模板。） */

  /* 左栏（导航 + 会话区 + 状态）：桌面常驻 288px；≤920px 在下方媒体里抽屉化 */
  .fg-rail {
    width: 288px;
    flex: none;
    display: flex;
    flex-direction: column;
    min-height: 0;
    /* v2.2：左栏比主区深一档（侧栏退后半步、主区是主角）。
       两家参考实现同款（侧栏 #f6f6f6/#f7f7f7 vs 主区 #fcfcfc/#fff）。 */
    background: ${tokens.softFill};
    border-right: 1px solid ${tokens.border};
  }

  /* 品牌 mark：圆角方形、accent 实心、白字「ark」（v2.2：不再用靛蓝→紫渐变） */
  .fg-rail-mark {
    width: 24px;
    height: 24px;
    flex: none;
    display: grid;
    place-items: center;
    border-radius: 7px;
    background: ${tokens.accent};
    color: ${tokens.onPrimary};
    font-size: 8.5px;
    font-weight: 700;
    letter-spacing: -0.03em;
    line-height: 1;
  }

  /* 品牌区：48px（原顶栏 52px，省下的 4px 留给会话区） */
  .fg-rail-brand {
    flex: none;
    display: flex;
    align-items: center;
    height: 48px;
    gap: 8px;
    padding: 0 14px;
    font-size: 15px;
    font-weight: 660;
    letter-spacing: -0.02em;
    color: ${tokens.text};
    text-decoration: none;
    white-space: nowrap;
  }

  /* 新建会话：品牌下方的全宽按钮（v14.1，对齐 dsh 左栏的首屏动作）。
     它比导航项高一档（38px / 品牌实心 / 白字）：这是"开始一件事"，不是"切一个视图"。
     2026-11-06 纠偏：旧版是 softFill 灰底 + hairline 边，在白侧栏上读起来像输入框/占位块；
     改为**品牌实心主按钮**（与全站 primary 同一口径），是左栏唯一的高强调动作。 */
  .fg-rail-new {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
    height: 32px;
    margin: 0 8px 2px;
    padding: 0 10px;
    border: 0;
    border-radius: 10px;
    background: transparent;
    color: ${tokens.text};
    font: inherit;
    font-size: 13.5px;
    font-weight: 500;
    text-align: left;
    cursor: pointer;
    user-select: none;
    transition: background 0.12s ease, color 0.12s ease;
  }
  /* v2.2：新建是**导航行**不是色块 CTA（参考实现里「新任务 / 新建任务」都是普通行）。
     左栏首屏因此只留一处色块：品牌 mark。 */
  .fg-rail-new:hover { background: ${tokens.surfaceActive}; }
  .fg-rail-new:active { background: ${tokens.surfaceActive}; }
  .fg-rail-new:focus-visible {
    outline: 2px solid ${tokens.accent};
    outline-offset: 2px;
  }
  /* /sessions 上它承载「你在这」：与导航项同一套选中语汇（底色 + 深字）。 */
  .fg-rail-new.fg-on {
    background: ${tokens.surfaceActive};
    color: ${tokens.text};
    font-weight: 600;
  }
  .fg-rail-new .fg-rail-new-icon {
    width: 14px;
    height: 14px;
    flex: none;
    display: grid;
    place-items: center;
    color: ${tokens.textSecondary};
  }
  .fg-rail-new.fg-on .fg-rail-new-icon { color: ${tokens.text}; }

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
  /* 选中态（设计 §16 / §17）：浅灰底 + 主色文字 + 主色图标，**不加粗**（与未选中同字重）。 */
  .fg-navitem.fg-on { background: ${tokens.softFill}; color: ${tokens.primary}; }
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

  /* 组行：整行只切换展开（组没有落地页），箭头只是视觉指示。
     宽 100%：组行现在是 button，在块级父容器里会按内容收缩，不给就会缩到文字宽度、
     点击热区与选中底都只剩一截（2026-11-05 实测 96px）。 */
  .fg-navgroup-row { width: 100%; padding-right: 4px; }
  .fg-navgroup-caret {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    color: ${tokens.textPlaceholder};
  }
  .fg-navgroup-row:hover .fg-navgroup-caret { color: ${tokens.textSecondary}; }

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
  /* 二级项选中：同主色文字 + 浅灰底，不加粗（设计 §17）。 */
  .fg-navsub.fg-on { background: ${tokens.softFill}; color: ${tokens.primary}; }
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
    /* 这栏永远不横滚（需求 5）：内容是单行省略的目录，横向滚动只可能是布局溢出。 */
    overflow-x: hidden;
  }

  /* 左栏 footer：固定左下，点「设置」直接开设置弹窗。这一行只有「设置」一个入口
     （WS 状态在窗口右下角的全局状态条，不重复一份）。
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

  /* 差集列表：逐条给出上游模型名 + 严重度 + 一句可执行的说明。 */
  .fg-difflist { margin: 0; padding-left: 16px; font-size: 12px; line-height: 1.7; }
  .fg-difflist li { display: flex; gap: 8px; align-items: baseline; }
  .fg-sev { flex: none; font-size: 11px; padding: 0 6px; border-radius: ${tokens.radiusSm}; }
  .fg-sev.fg-error { background: ${tokens.primarySoft}; color: ${tokens.error}; }
  .fg-sev.fg-degrade { background: ${tokens.warningBg}; color: ${tokens.warning}; }
  .fg-sev.fg-info { background: ${tokens.softFill}; color: ${tokens.textPlaceholder}; }

  /* 「底座独有」折叠区：默认非空（底座自带的内置目录），所以它不该长得像报错。 */
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
    color: ${tokens.success};
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
  .fg-rowtag.fg-warn { border-color: ${tokens.warningBorder}; background: ${tokens.warningBg}; color: ${tokens.warning}; }
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
    border: 1px solid ${tokens.borderStrong};
    border-radius: 8px;
    background: ${tokens.surface};
    color: ${tokens.text};
    cursor: pointer;
  }
  .fg-burger:hover { background: ${tokens.softFill}; border-color: ${tokens.borderHover}; }
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
    border-color: ${tokens.accent};
    box-shadow: 0 0 0 3px ${tokens.accentSoft};
  }

  /* 输入卡下方一行（原型 §13）：工作区 chip / 权限 chip 靠左；发送键提示已撤掉（2026-11-05）。 */
  .fg-composer-chips {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-top: 8px;
    padding: 0 2px;
    flex-wrap: wrap;
  }

  /* 通用 chip（工作区 / 权限）：pill 形态，走 token。 */
  .fg-chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 32px;
    padding: 0 11px;
    border-radius: ${tokens.radiusPill}px;
    border: 1px solid transparent;
    background: none;
    font: inherit;
    font-size: 12px;
    color: ${tokens.textSecondary};
    white-space: nowrap;
    cursor: pointer;
    transition: background 0.12s ease, border-color 0.12s ease, color 0.12s ease;
  }
  .fg-chip:hover:not(:disabled) { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-chip:disabled { cursor: default; opacity: 0.55; }
  .fg-chip-ico { display: inline-flex; align-items: center; font-size: 13px; color: ${tokens.textPlaceholder}; }
  .fg-chip-label { line-height: 1; }
  .fg-chip-chev { font-size: 10px; color: ${tokens.textPlaceholder}; }
  .fg-chip.fg-on { color: ${tokens.primary}; }
  .fg-chip.fg-on .fg-chip-ico { color: ${tokens.primary}; }
  /* 权限 chip 菜单项：一行 + 说明副行。 */
  .fg-perm-opt { display: flex; flex-direction: column; gap: 1px; padding: 2px 0; }
  .fg-perm-opt-name { font-size: 13px; line-height: 1.4; }
  .fg-perm-opt-desc { font-size: 11.5px; color: ${tokens.textPlaceholder}; line-height: 1.4; }

  /* 模型选择器关闭态：药丸（不落表单框感）——hairline 边 + 全圆角 + mono 小字，
     与同一行的「在项目中工作」弱字、圆形发送钮同一语言；hover 抬边+软底，聚焦出靛蓝环。 */
  .fg-model-pill {
    width: auto;
    min-width: 132px;
    max-width: 240px;
    height: 28px;
    padding: 0 10px;
    gap: 6px;
    border: 1px solid ${tokens.borderStrong};
    border-radius: 999px;
    background: ${tokens.surface};
    cursor: pointer;
    transition: border-color 0.12s ease, background 0.12s ease, box-shadow 0.12s ease, min-width 0.12s ease;
  }
  .fg-model-pill:hover {
    border-color: ${tokens.borderHover};
    background: ${tokens.softFill};
  }
  /* 聚焦/展开：静息态贴合文案（不长出一片空白），一旦交互就展开到可读可搜的宽度。 */
  .fg-model-pill:focus-within {
    min-width: 216px;
    border-color: ${tokens.accent};
    box-shadow: 0 0 0 3px ${tokens.accentSoft};
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
     候选多时（实测通告过 20 余条）它是用户判断"这些命令从哪来"的唯一线索。 */
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

  /* 工作空间 chip（DSH 的 WorkspaceChip 形态）：贴在输入卡**上沿左上角**的一行，
     静息无边框无底色（它不是表单控件，是"我在哪工作"的事实），hover / 展开才出软底。
     与 fg-ws-pill 的分工：pill 是底栏里的表单控件语言，chip 是卡外的事实语言。 */
  .fg-ws-chiprow {
    display: flex;
    align-items: center;
    min-width: 0;
    padding-left: 2px;
    /* 与卡片之间只留 6px：chip 读作卡片的标题行，不是另一个区块 */
    margin-bottom: 6px;
  }
  .fg-ws-chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    max-width: 360px;
    min-height: 28px;
    padding: 0 8px;
    border: none;
    border-radius: 8px;
    background: transparent;
    cursor: pointer;
    font-family: inherit;
    font-size: 13px;
    font-weight: 500;
    line-height: 20px;
    color: ${tokens.text};
    transition: background 0.12s ease;
  }
  /* hover / 展开只有**可点的那一个**（按钮）才吃：只读态是同一个类的 span，
     它没有 :disabled 可匹配，若把 hover 写在类上会照样亮起来——"不可点"必须由形态保证，
     不能靠调用方自觉。展开态（aria-expanded）也只有选择器的按钮会有。 */
  button.fg-ws-chip:hover:not(:disabled),
  .fg-ws-chip[aria-expanded='true'] { background: ${tokens.softFill}; }
  /* 只读态（已建会话：工作空间创建时定稿、之后不可变）：静态回声——无 hover 反馈、无指针暗示，
     文案保持满对比度（不让它看着像"禁用/不可用"，它是事实不是按钮）。 */
  .fg-ws-chip-static { cursor: default; }
  .fg-ws-chip:disabled { cursor: default; }
  .fg-ws-chip-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
    border: 1px solid ${tokens.borderStrong};
    border-radius: 999px;
    background: ${tokens.surface};
    cursor: pointer;
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    color: ${tokens.text};
    transition: border-color 0.12s ease, background 0.12s ease;
  }
  .fg-ws-pill:hover:not(:disabled) {
    border-color: ${tokens.borderHover};
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
  .fg-cap-mode-btn:hover:not(:disabled) { color: ${tokens.text}; }
  .fg-cap-mode-btn:disabled { cursor: default; opacity: 0.55; }
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
  .fg-cap-mini:hover:not(:disabled) { text-decoration: underline; }
  .fg-cap-mini:disabled { cursor: default; opacity: 0.55; }
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

  /* ——— 会话内改能力（M3-8）：输入区的 `+` 菜单与能力 chip ———
     默认态只有一颗 `+`（没有任何 chip）：这是"这条会话没限制能力"的如实样子，不是省掉了一层。 */
  .fg-cap-bar { display: inline-flex; align-items: center; gap: 6px; min-width: 0; }
  .fg-cap-add {
    display: inline-flex; align-items: center; justify-content: center;
    width: 24px; height: 24px; padding: 0;
    border: 1px solid ${tokens.borderStrong}; border-radius: 999px;
    background: ${tokens.surface}; color: ${tokens.textSecondary};
    cursor: pointer; transition: border-color 0.12s ease, background 0.12s ease, color 0.12s ease;
  }
  .fg-cap-add:hover:not(:disabled) { border-color: ${tokens.borderHover}; background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-cap-add:disabled { cursor: default; opacity: 0.55; }
  /* chip 只在"声明过"的那一格出现：专家用名字首字当头图，技能/连接器带数量。 */
  .fg-cap-chip {
    display: inline-flex; align-items: center; gap: 5px;
    height: 24px; max-width: 200px; padding: 0 8px;
    border: 1px solid ${tokens.borderStrong}; border-radius: 999px;
    background: ${tokens.surface}; color: ${tokens.textSecondary};
    cursor: pointer; font-size: 11.5px;
    transition: border-color 0.12s ease, background 0.12s ease, color 0.12s ease;
  }
  .fg-cap-chip:hover:not(:disabled) { border-color: ${tokens.borderHover}; background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-cap-chip:disabled { cursor: default; opacity: 0.55; }
  .fg-cap-chip-avatar {
    display: inline-flex; align-items: center; justify-content: center;
    width: 16px; height: 16px; border-radius: 999px;
    background: ${tokens.softFill}; color: ${tokens.text};
    font-size: 10px; line-height: 1;
  }
  .fg-cap-chip-ico { display: inline-flex; color: ${tokens.textPlaceholder}; }
  .fg-cap-chip-label {
    min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    color: ${tokens.text};
  }
  .fg-cap-chip-count {
    font-family: ${tokens.fontMono}; font-size: 10.5px; color: ${tokens.textPlaceholder};
  }
  .fg-cap-chip-chev { font-size: 9px; color: ${tokens.textPlaceholder}; }
  /* `+` 菜单（专家 / 技能 / 连接器）：一行一项 + 右侧 chevron，与工作空间弹层同一套语言。 */
  .fg-cap-menu { display: flex; flex-direction: column; min-width: 200px; }
  .fg-cap-menu-item {
    display: flex; align-items: center; gap: 8px;
    padding: 6px 10px; border: none; border-radius: 8px;
    background: transparent; cursor: pointer; text-align: left;
    font-size: 12.5px; color: ${tokens.text};
    transition: background 0.12s ease;
  }
  .fg-cap-menu-item:hover { background: ${tokens.softFill}; }
  .fg-cap-menu-item.fg-pending { cursor: default; color: ${tokens.textPlaceholder}; }
  .fg-cap-menu-item.fg-pending:hover { background: transparent; }
  .fg-cap-menu-ico { display: inline-flex; color: ${tokens.textPlaceholder}; }
  .fg-cap-menu-label { flex: 1; min-width: 0; }
  .fg-cap-menu-chev { color: ${tokens.textPlaceholder}; font-size: 12px; }
  .fg-cap-menu-sep { height: 1px; background: ${tokens.border}; margin: 4px 6px; }
  /* 「模式」= 待开发：右侧灰色小标签（与「添加能力」菜单同一处）。 */
  .fg-cap-menu-soon {
    margin-left: auto; font-size: 10px; line-height: 15px; color: ${tokens.textPlaceholder};
    border: 1px solid ${tokens.border}; border-radius: 5px; padding: 0 6px; font-family: ${tokens.fontMono};
  }
  /* 专家行在输入区是 <button>（没有勾选框），把浏览器默认样式抹平。 */
  button.fg-cap-opt {
    width: 100%; border: none; background: transparent; cursor: pointer; text-align: left;
    font: inherit;
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

  /* 行内图标按钮（上级 / 主目录 / 左栏管理入口共用一套静息形态）。 */
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

  /* ——— 目录浏览对话框（v14.3，对齐 dsh 的 DirectoryBrowser） ———
     680×500 的居中弹窗，头部=面包屑+路径编辑入口，主体=Miller 双栏，底部=动作条。
     尺寸用固定档而不是百分比：双栏在窄容器里放不下（每栏下限 240px），
     而这是"选一个目录"的专用对话框，占满整屏反而丢失上下文。 */
  .fg-ws-dlg .ant-modal-content { padding: 0; }
  .fg-ws-dlg .ant-modal-header { padding: 16px 24px 8px; margin: 0; border-bottom: none; }
  .fg-ws-dlg .ant-modal-body { padding: 0 24px 0; }
  .fg-ws-dlg .ant-modal-footer {
    margin: 0;
    padding: 12px 24px;
    border-top: 1px solid ${tokens.border};
  }
  .fg-ws-dlg-body {
    display: flex;
    flex-direction: column;
    height: 380px; /* 500 = 头部 + 主体 + 动作条；主体给双栏留够高度 */
    min-height: 0;
  }
  /* 上级 / 主目录两颗图标与面包屑同排（右侧）；列表内容多寡都不影响它们的位置。 */
  /* 面包屑条：链可横向滚动（深路径下保尾段在视野内），右端是编辑入口与两颗图标。 */
  .fg-ws-crumbbar { display: flex; align-items: center; gap: 2px; flex: none; }
  .fg-ws-crumbs {
    display: flex;
    align-items: center;
    gap: 2px;
    flex: 1;
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .fg-ws-crumbseat { display: inline-flex; align-items: center; gap: 2px; flex: none; }
  .fg-ws-crumb {
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    border: none;
    background: transparent;
    padding: 2px 4px;
    border-radius: 6px;
    cursor: pointer;
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    color: ${tokens.textSecondary};
  }
  .fg-ws-crumb:hover:not(:disabled) { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-ws-crumb:disabled { cursor: default; opacity: 0.5; }
  .fg-ws-crumbchev { flex: none; font-size: 10px; color: ${tokens.textPlaceholder}; }
  /* 铅笔区：一个**看得见**的入口（隐形热区是这里唯一能打字的路，不能靠猜）。 */
  .fg-ws-crumbedit {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: 24px;
    height: 24px;
    border: none;
    border-radius: 6px;
    background: transparent;
    cursor: text;
    font-size: 12px;
    color: ${tokens.textPlaceholder};
  }
  .fg-ws-crumbedit:hover:not(:disabled) { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-ws-crumbedit:disabled { cursor: default; opacity: 0.35; }
  .fg-ws-pathinput { flex: 1; font-family: ${tokens.fontMono}; font-size: 11.5px; }
  /* 完整路径一行：面包屑会被长路径挤到只剩尾段，这里给一字不差的全文。 */
  .fg-ws-dlg-path {
    flex: none;
    padding: 4px 0 6px;
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    color: ${tokens.textPlaceholder};
    overflow-x: auto;
    white-space: nowrap;
    scrollbar-width: none;
  }
  /* Miller 双栏：每栏下限 240px，放不下时整行横向滚动（而不是把两栏挤成一条缝）。
     高度按内容自适应（内容多时列内滚动）；不足的那部分由下面的文件小节与"弹簧"吸收——
     固定 flex:1 会让"只有两行目录"的空目录出现一大片空白。 */
  .fg-ws-miller {
    display: flex;
    align-items: stretch;
    flex: 0 1 auto;
    min-height: 64px;
    max-height: 220px;
    gap: 10px;
    overflow-x: auto;
  }
  .fg-ws-col {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1 1 auto;
    min-width: 240px;
    min-height: 24px;
    overflow-y: auto;
    padding-right: 4px;
  }
  /* 单栏且空时：让"此目录下没有子目录"就在列表的位置显示，而不是被推到容器底部。 */
  .fg-ws-miller.fg-single {
    flex-direction: column;
    min-height: 0;
    overflow-x: hidden;
  }
  /* 弹簧：把"上级/主目录"与动作条顶到最下沿——列表内容多寡都不改变它们的位置。 */
  .fg-ws-grow { flex: 1 1 auto; min-height: 0; }
  .fg-ws-divider { flex: none; width: 1px; background: ${tokens.border}; }
  .fg-ws-rowseat { display: flex; flex: none; }
  /* 选中态：柔性底 + 主色开文件夹图标（与 hover 区分得开——hover 是灰底，选中是主色底）。 */
  .fg-ws-rowsel, .fg-ws-rowsel:hover:not(:disabled) { background: ${tokens.selectedBg}; }
  .fg-ws-icon-btn.fg-ws-open, .fg-ws-open { color: ${tokens.primary}; }
  .fg-ws-item-chev { flex: none; font-size: 10px; color: ${tokens.textPlaceholder}; }
  .fg-ws-roots { display: flex; flex-direction: column; gap: 2px; flex: none; }
  .fg-ws-files { display: flex; flex-direction: column; gap: 2px; max-height: 160px; overflow-y: auto; flex: none; }
  /* 错误行与"目录太多"提示贴着列表下沿，然后被 nav 的 margin-top:auto 推到动作条上方。 */
  .fg-ws-note {
    flex: none;
    padding: 2px 0;
    font-size: 11.5px;
    color: ${tokens.textPlaceholder};
  }
  /* 底部动作条：新建文件夹 / 显示隐藏文件在左，取消 / 确认在右（对齐 dsh 的 footerBar）。 */
  .fg-ws-foot { display: flex; align-items: center; gap: 8px; }
  .fg-ws-foot-gap { flex: 1; }
  .fg-ws-toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: none;
    background: transparent;
    padding: 0;
    cursor: pointer;
    font-size: 12.5px;
    color: ${tokens.textSecondary};
    white-space: nowrap;
  }
  .fg-ws-toggle:hover:not(:disabled) { color: ${tokens.text}; }
  .fg-ws-toggle:disabled { cursor: default; opacity: 0.5; }
  .fg-ws-toggle.fg-on { color: ${tokens.text}; }
  .fg-ws-create { display: flex; flex-direction: column; gap: 10px; }
  .fg-ws-create-in { font-size: 12.5px; color: ${tokens.textSecondary}; }
  .fg-ws-empty {
    padding: 4px 8px;
    font-size: 11.5px;
    color: ${tokens.textPlaceholder};
  }

  /* v14：创建/登记失败**就地**报错（设计 §5.2 状态 E）——不关弹层，用户当前浏览位置还在。
     不做成 toast 是因为那种提示会盖住目录树，而用户要做的正是"换一个目录"。 */
  .fg-ws-error {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    margin: 4px 6px 6px;
    padding: 6px 9px;
    border-radius: 8px;
    background: ${tokens.warningBg};
    color: ${tokens.error};
    font-size: 11.5px;
    line-height: 1.5;
    word-break: break-all;
  }
  /* 不可写时的一句原因（替代那个不出现的「添加工作空间…」按钮）。 */
  .fg-ws-hint {
    padding: 6px 9px 8px;
    font-size: 11.5px;
    color: ${tokens.textPlaceholder};
    line-height: 1.6;
  }
  /* 左栏分组行上的行内动作（v14.1：「⋯」管理 + 「⊕」在此新建）：**一起**悬停浮现。
     未悬停时整行只剩"名字 + 会话数"，工作空间名不再被一排图标挤掉；
     触屏（≤920px 的抽屉态）没有 hover，所以那一档常显——否则这两个入口等于不存在。 */
  /* 行内动作用 visibility 而不是 display 隐藏：**位置一直占着**，所以鼠标移上去
     不会把名字挤窄、也不会让会话数消失——整行唯一的悬停变化就是这两颗按钮出现。
     fg-menu-open 那条抄的是 dsh 的 .projectRow.menuOpen .rowActions：菜单打开时
     鼠标往往已经移到浮层上，没有它的话锚点按钮会跟着消失（浮层还在，锚点却空了）。 */
  .fg-ws-manage { flex: none; display: inline-flex; gap: 2px; visibility: hidden; }
  .fg-wsgroup-row:hover .fg-ws-manage,
  .fg-wsgroup-row.fg-menu-open .fg-ws-manage { visibility: visible; }
  .fg-wsgroup-row .fg-ws-icon-btn {
    width: 20px;
    height: 20px;
    border: 0;
    background: transparent;
    border-radius: 5px;
    color: ${tokens.textSecondary};
    cursor: pointer;
    display: grid;
    place-items: center;
    font-size: 11.5px;
  }
  .fg-wsgroup-row .fg-ws-icon-btn:hover:not(:disabled) { background: ${tokens.softFill}; color: ${tokens.text}; }
  .fg-wsgroup-row .fg-ws-icon-btn:disabled { cursor: default; opacity: 0.35; }
  /* ——— 左栏「工作区」区头（v14.2） ———
     几何与行为对齐 dsh 的 sectionHeader：高 36px、gap 4、overflow hidden；
     **搜索展开时标题与动作两颗一起收起**，输入框占据整条区头。
     之所以要 overflow:hidden：收起靠的是 max-width 过渡而不是卸载 DOM，
     不裁掉溢出的话，宽度收缩期间图标会溢到右边压住列表。 */
  /* 宽屏会话信息栏（v2.2，≥1600 才出现）。
     1912 宽窗口下转录列只有 840，两侧各留 362 空白；把右侧空白做成「会话事实」，
     比把正文拉到 1200 宽（行长失控）更合理。窄屏不渲染（display:none）。 */
  .fg-chat-side { display: none; }
  @media (min-width: 1600px) {
    .fg-chat-side {
      display: flex;
      flex-direction: column;
      gap: 14px;
      width: 320px;
      flex: none;
      min-height: 0;
      overflow-y: auto;
      padding: 16px;
      border-left: 1px solid ${tokens.border};
      background: ${tokens.surface};
    }
  }
  .fg-chat-side-h {
    font-family: ${tokens.fontMono};
    font-size: 10px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: ${tokens.textPlaceholder};
  }
  .fg-chat-side-row {
    display: flex;
    gap: 8px;
    padding: 6px 0;
    border-top: 1px solid ${tokens.border};
    font-size: 12px;
    line-height: 19px;
  }
  .fg-chat-side-row:first-of-type { border-top: 0; }
  .fg-chat-side-k { flex: none; width: 62px; color: ${tokens.textPlaceholder}; font-family: ${tokens.fontMono}; font-size: 10.5px; }
  .fg-chat-side-v { flex: 1; min-width: 0; color: ${tokens.text}; overflow-wrap: anywhere; }

  /* 用量页总览四卡（v2.2）：宽屏 4 列 → ≤1280 两列 → ≤760 单列。
     原来是写死的 repeat(4, minmax(0,1fr))，窄窗口上每格被压到看不清数字。 */
  .fg-stats {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 1px;
  }
  @media (max-width: 1280px) { .fg-stats { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  @media (max-width: 760px) { .fg-stats { grid-template-columns: minmax(0, 1fr); } }

  /* 会话 / 归档：13px 下划 tab（v2.2）。
     此前两者藏在「视图选项」下拉里——高频切换不该走第二层。 */
  .fg-rail-tabs {
    flex: none;
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 0 12px;
  }
  .fg-rail-tab {
    position: relative;
    height: 28px;
    padding: 0;
    border: 0;
    background: transparent;
    color: ${tokens.textPlaceholder};
    font: inherit;
    font-size: 13px;
    font-weight: 400;
    cursor: pointer;
    transition: color 0.12s ease;
  }
  .fg-rail-tab:hover { color: ${tokens.textSecondary}; }
  .fg-rail-tab.fg-on { color: ${tokens.text}; font-weight: 600; }
  .fg-rail-tab.fg-on::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: -1px;
    height: 2px;
    border-radius: 2px;
    background: ${tokens.text};
  }
  .fg-rail-tab-n { margin-left: 4px; font-family: ${tokens.fontMono}; font-size: 10px; color: ${tokens.textPlaceholder}; }
  .fg-rail-tab:focus-visible { outline: 2px solid ${tokens.accent}; outline-offset: 2px; border-radius: 4px; }

  .fg-rail-sechead {
    flex: none;
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 4px;
    height: 36px;
    padding: 0 8px 0 12px;
    overflow: hidden;
  }
  .fg-rail-sechead-label {
    flex: none;
    max-width: 45%;
    min-width: 0;
    overflow: hidden;
    white-space: nowrap;
    font-size: 12px;
    font-weight: 620;
    letter-spacing: 0.02em;
    color: ${tokens.textPlaceholder};
    transition: max-width 0.18s ease, opacity 0.12s ease, margin-right 0.18s ease;
  }
  .fg-rail-sechead.fg-searching .fg-rail-sechead-label { max-width: 0; opacity: 0; margin-right: -4px; }

  /* 折叠搜索：始终占一个图标位（28px），展开后吃掉整条区头的剩余宽度。 */
  .fg-rail-sesearch {
    flex: 1;
    max-width: 28px;
    min-width: 0;
    display: flex;
    align-items: center;
    margin-left: auto;
    transition: max-width 0.18s ease;
  }
  .fg-rail-sechead.fg-searching .fg-rail-sesearch { max-width: 100%; }
  .fg-rail-sesearch-box {
    display: flex;
    align-items: center;
    width: 100%;
    height: 28px;
    border-radius: 8px;
    transition: border-color 0.18s ease, background-color 0.18s ease;
  }
  .fg-rail-sechead.fg-searching .fg-rail-sesearch-box {
    border: 1px solid ${tokens.border};
    background: ${tokens.surface};
    padding-right: 2px;
  }
  .fg-rail-seinput {
    flex: 1;
    width: 0;
    min-width: 0;
    border: 0;
    outline: none;
    background: transparent;
    font: inherit;
    font-size: 12.5px;
    color: ${tokens.text};
    opacity: 0;
    pointer-events: none;
    transition: opacity 0.12s ease;
  }
  .fg-rail-seinput::placeholder { color: ${tokens.textPlaceholder}; }
  .fg-rail-sechead.fg-searching .fg-rail-seinput { opacity: 1; pointer-events: auto; }
  .fg-rail-sechead.fg-searching .fg-rail-sechead-icon.fg-searchbtn:hover { background: transparent; }

  .fg-rail-sechead-actions {
    flex: none;
    display: flex;
    align-items: center;
    gap: 2px;
    max-width: 64px;
    overflow: hidden;
    transition: max-width 0.18s ease, opacity 0.12s ease;
  }
  .fg-rail-sechead.fg-searching .fg-rail-sechead-actions {
    max-width: 0;
    opacity: 0;
    pointer-events: none;
  }
  .fg-rail-sechead-icon {
    width: 28px;
    height: 28px;
    flex: none;
    display: inline-grid;
    place-items: center;
    border: 0;
    background: transparent;
    border-radius: 8px;
    color: ${tokens.textSecondary};
    cursor: pointer;
    font-size: 13px;
  }
  .fg-rail-sechead-icon:hover:not(:disabled) { background: ${tokens.softFill}; color: ${tokens.text}; }
  /* 「清除搜索」只在展开态出现。
     选择器**必须**带上 .fg-rail-sechead-icon：那个类也声明了 display，两者同权重时
     由源码顺序决定谁赢——v14.2 初版就栽在这里，于是那个 ✕ 常驻在区头、从折叠的搜索框里
     溢出来正好压在「视图选项」图标上，看起来像"分组图标上有个叉"。带上第二个类权重就
     高于它，改顺序也不会再翻车。 */
  .fg-rail-sechead-icon.fg-rail-seclear { display: none; }
  .fg-rail-sechead.fg-searching .fg-rail-sechead-icon.fg-rail-seclear { display: inline-grid; }
  /* 不可写（memory 持久层）时区头不渲染「添加工作区」——不留一个必然失败的按钮。 */
  .fg-rail-sechead-icon:disabled { cursor: default; opacity: 0.35; }

  /* 左栏工作空间分组行（设计 §17：与会话名**同规格** 13px / 450 / text，不再是更重的组头）。 */
  .fg-wsgroup-row {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 5px 8px 5px 8px;
    border-radius: 8px;
    cursor: pointer;
    font-size: 13px;
    font-weight: 450;
    color: ${tokens.text};
    /* 内容再多也不外溢：名字是单行省略，动作按钮一直占位（需求 5 的第二道保险）。 */
    overflow: hidden;
    transition: background 0.12s ease;
  }
  .fg-wsgroup-row:hover { background: ${tokens.softFill}; }
  /* 展开态不再靠箭头：底色 + 组内会话行左缩进（需求 3）。
     含当前会话的组用主色名——"你在哪"必须在未悬停时也看得见。 */
  .fg-wsgroup-row.fg-open { background: ${tokens.softFill}; }
  .fg-wsgroup-row.fg-current .fg-wsgroup-name { color: ${tokens.primary}; }
  .fg-wsgroup-body { padding-left: 10px; }
  /* 目录不可用时的警示图标：窄槽，不再承担方向指示。 */
  .fg-wsgroup-lead {
    flex: none;
    display: inline-flex;
    align-items: center;
    font-size: 11px;
    color: ${tokens.warning};
  }
  .fg-wsgroup-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .fg-wsgroup-name.fg-unnamed { font-style: italic; font-weight: 500; color: ${tokens.textSecondary}; }
  .fg-wsgroup-warn { flex: none; color: ${tokens.warning}; font-size: 11.5px; }
  /* 组内配额：「展开其余 N 个会话」/「收起」——一行文字按钮，不做成大块空态。 */
  .fg-wsgroup-more {
    display: flex;
    align-items: center;
    gap: 5px;
    width: calc(100% - 36px);
    margin: 1px 8px 3px 28px;
    padding: 4px 8px;
    border: 0;
    background: transparent;
    border-radius: 8px;
    color: ${tokens.textPlaceholder};
    font: inherit;
    font-size: 11.5px;
    cursor: pointer;
    text-align: left;
  }
  .fg-wsgroup-more:hover { background: ${tokens.softFill}; color: ${tokens.textSecondary}; }
  .fg-wsgroup-more .anticon { font-size: 10px; }
  /* 分组内提示（目录不可用 / 还没有会话）：只有一行字，不做成大块空态。 */
  .fg-wsgroup-note {
    padding: 2px 8px 6px 28px;
    font-size: 11px;
    color: ${tokens.textPlaceholder};
    line-height: 1.6;
  }

  /* 会话行右端（设计 §15；2026-11-05 反馈改成「状态点 + 日期」）：紧邻「⋯」、不参与收缩。
     「⋯」与它共用行右端——静止态在最右，hover / 当前行时整格淡出让位，位置不跳（D11）。
     颜色只落在**点**上：日期是中性事实，不该被运行中/失败的语义染成主色或红色。
     六态见 railView.sessionBadge；已完成用 success（不再用灰——灰读起来像"没有状态"）。 */
  .fg-sess-status {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    line-height: 1;
    color: ${tokens.textPlaceholder};
    white-space: nowrap;
    transition: opacity 0.12s ease;
  }
  .fg-sess-dot {
    width: 6px;
    height: 6px;
    flex: none;
    border-radius: 50%;
    background: ${tokens.textPlaceholder};
  }
  /* 日期：等宽数字，避免不同日期让右端宽度跳动。 */
  .fg-sess-date {
    font-variant-numeric: tabular-nums;
    letter-spacing: 0.01em;
  }
  .fg-sess-status.fg-run .fg-sess-dot { background: ${tokens.primary}; animation: fg-pulse 1.4s ease-in-out infinite; }
  .fg-sess-status.fg-wait .fg-sess-dot { background: ${tokens.warning}; }
  .fg-sess-status.fg-fail .fg-sess-dot { background: ${tokens.error}; }
  .fg-sess-status.fg-done .fg-sess-dot { background: ${tokens.success}; }
  /* 已取消：作废，什么都没留下——中性灰（不是红：取消不是失败）。 */
  .fg-sess-status.fg-cancel .fg-sess-dot { background: ${tokens.textPlaceholder}; }
  /* 已中断：琥珀**空心环**，与「待授权」的实心琥珀同族但形态不同（可重发 ≠ 正在等你）。 */
  .fg-sess-status.fg-interrupt .fg-sess-dot {
    background: transparent;
    box-shadow: inset 0 0 0 1.5px ${tokens.warning};
  }
  .fg-sess-item:hover .fg-sess-status,
  .fg-sess-item.fg-on .fg-sess-status { opacity: 0; }

  /* 定时任务列表卡行（原型 §F 的 .auto-row）：图标 + 主体 + 动作三列。 */
  .fg-auto-row {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px;
    border: 1px solid ${tokens.border};
    border-radius: ${tokens.radiusMd};
    background: ${tokens.surface};
    transition: border-color 0.12s ease;
  }
  .fg-auto-row:hover { border-color: ${tokens.borderStrong}; }
  .fg-auto-ico {
    width: 40px;
    height: 40px;
    border-radius: 10px;
    flex: none;
    display: grid;
    place-items: center;
    font-size: 18px;
    background: ${tokens.primarySoft};
    color: ${tokens.primary};
  }
  .fg-auto-main { flex: 1; min-width: 0; }
  .fg-auto-name {
    font-size: 14px;
    font-weight: 650;
    color: ${tokens.text};
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  .fg-auto-name > span:first-child { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fg-auto-sched { font-size: 12.5px; color: ${tokens.textSecondary}; margin-top: 2px; }
  .fg-auto-next {
    font-size: 11px;
    font-family: ${tokens.fontMono};
    color: ${tokens.textPlaceholder};
    margin-top: 3px;
  }
  .fg-auto-act { display: flex; gap: 8px; flex: none; align-items: center; flex-wrap: wrap; justify-content: flex-end; }

  /* 资料库节点卡行（原型 §E 的 .mat-row）：图标 + 名称/类型·大小 + 更新时间 + 删除。 */
  .fg-mat-list { display: flex; flex-direction: column; }
  .fg-mat-row {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 8px;
    border-bottom: 1px solid ${tokens.border};
  }
  .fg-mat-row:hover { background: ${tokens.codeBg}; }
  .fg-mat-ico {
    width: 34px;
    height: 34px;
    flex: none;
    border-radius: 8px;
    display: grid;
    place-items: center;
    font-size: 16px;
    background: ${tokens.softFill};
    color: ${tokens.textSecondary};
  }
  .fg-mat-main { flex: 1; min-width: 0; }
  .fg-mat-name { font-size: 13.5px; font-weight: 600; color: ${tokens.text}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fg-mat-meta { font-size: 11.5px; color: ${tokens.textPlaceholder}; font-family: ${tokens.fontMono}; margin-top: 1px; }
  .fg-mat-when { flex: none; font-size: 11.5px; color: ${tokens.textPlaceholder}; font-family: ${tokens.fontMono}; white-space: nowrap; }

  /* —— 能力卡片网格（专家 / 技能 / 连接器三页共用，2026-11-06 起由行列表改卡片式）——
     单面白卡：头像块 + 名称/标签 → 描述 → hairline + 元信息/动作；hover 仅抬边+轻阴影，不上移。 */
  .fg-cap-grid { display: grid; gap: 14px; padding: 18px; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); }
  .fg-cap-card {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 16px;
    background: ${tokens.surface};
    border: 1px solid ${tokens.border};
    border-radius: ${tokens.radiusLg}px;
    transition: border-color 0.14s ease, box-shadow 0.14s ease;
  }
  .fg-cap-card:hover { border-color: ${tokens.borderStrong}; box-shadow: 0 6px 18px rgba(17, 24, 39, 0.06); }
  .fg-cap-card.is-dim { opacity: 0.62; }
  .fg-cap-top { display: flex; align-items: flex-start; gap: 11px; min-width: 0; }
  .fg-cap-ava {
    width: 38px;
    height: 38px;
    flex: none;
    border-radius: 9px;
    display: grid;
    place-items: center;
    background: ${tokens.softFill};
    border: 1px solid ${tokens.border};
    color: ${tokens.textSecondary};
    font-size: 18px;
  }
  .fg-cap-nm { flex: 1; min-width: 0; }
  .fg-cap-name { font-size: 14px; font-weight: 650; letter-spacing: -0.005em; color: ${tokens.text}; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fg-cap-tags { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 5px; }
  .fg-cap-desc {
    font-size: 12.5px;
    line-height: 1.55;
    color: ${tokens.textSecondary};
    min-height: 38px;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .fg-cap-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    padding-top: 11px;
    margin-top: auto;
    border-top: 1px solid ${tokens.border};
    font-size: 11px;
    color: ${tokens.textPlaceholder};
  }
  .fg-cap-meta { flex: 1; min-width: 0; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; overflow: hidden; }
  .fg-cap-acts { display: flex; align-items: center; gap: 4px; flex: none; }
  .fg-cap-sep { width: 1px; height: 9px; background: ${tokens.borderStrong}; flex: none; }

  /* 危险操作图标按钮（全站统一「删除」外观）：静息即红字一眼可辨，hover 才染红底红边；
     与次按钮同尺寸/圆角，不投影。用于能力卡片、资料库等行内删除位。 */
  .fg-icon-btn {
    width: 28px;
    height: 28px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid transparent;
    background: transparent;
    border-radius: ${tokens.radiusSm}px;
    color: ${tokens.textPlaceholder};
    cursor: pointer;
    transition: background 0.12s ease, color 0.12s ease, border-color 0.12s ease;
  }
  .fg-icon-btn:hover { background: ${tokens.softFill}; color: ${tokens.text}; border-color: ${tokens.border}; }
  .fg-icon-btn.fg-icon-danger { color: ${tokens.error}; }
  .fg-icon-btn.fg-icon-danger:hover { background: ${tokens.dangerSoft}; color: ${tokens.error}; border-color: ${tokens.dangerBorder}; }
  .fg-icon-btn:disabled { cursor: default; opacity: 0.5; }

  /* 会话信息浮层（悬停会话行弹出，设计 §13–§19）：白卡 + hairline + 阴影（浮层允许阴影）。
     内容四行：标题 / 所属空间 / 模型 / 更新时间（空行不渲染）。 */
  .fg-sess-info {    min-width: 220px;
    max-width: 320px;
    padding: 2px 0;
  }
  .fg-sess-info-title {
    font-size: 13.5px;
    font-weight: 600;
    color: ${tokens.text};
    margin-bottom: 8px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .fg-sess-info-row {
    display: flex;
    align-items: baseline;
    gap: 10px;
    padding: 3px 0;
    font-size: 12.5px;
    line-height: 1.5;
  }
  .fg-sess-info-key { flex: none; color: ${tokens.textPlaceholder}; }
  .fg-sess-info-val {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: ${tokens.text};
  }

  /* 会话行「更多」按钮：悬停浮现；触屏（移动抽屉态）常显 */
  .fg-sess-more { opacity: 0; transition: opacity 0.12s ease; }
  .fg-sess-item:hover .fg-sess-more,
  .fg-sess-more.fg-keep { opacity: 1; }
  @media (max-width: 920px) {
    .fg-sess-more { opacity: 1; }
    /* 触屏（抽屉态）没有 hover：动作按钮常显（位置本来就一直占着）。 */
    .fg-ws-manage { visibility: visible; }
  }

  /* 发送 / 停止同一颗 32px 圆钮（需求 6 上半）：三态只换图标与底色，**不位移、不降透明度**。
     空闲空文本时禁用的是箭头（仍看得清），等待授权/回答时是"禁用停止"并靠 title 说明原因。 */
  .fg-send-btn {
    flex: none;
    width: 32px;
    height: 32px;
    box-sizing: border-box;
    display: inline-grid;
    place-items: center;
    border: 0;
    border-radius: 50%;
    background: ${tokens.accent};
    color: ${tokens.onPrimary};
    font: inherit;
    cursor: pointer;
    transition: background 0.12s ease, color 0.12s ease;
  }
  .fg-send-btn:hover:not(:disabled) { background: ${tokens.accentHover}; }
  .fg-send-btn:active:not(:disabled) { background: ${tokens.accentActive}; }
  .fg-send-btn:focus-visible { outline: 2px solid ${tokens.accent}; outline-offset: 2px; }
  .fg-send-btn:disabled { cursor: default; opacity: 1; }
  .fg-send-btn:disabled:not(.fg-send-stop) { background: ${tokens.softFill}; color: ${tokens.textPlaceholder}; }
  .fg-send-btn.fg-send-stop { background: ${tokens.error}; color: ${tokens.onPrimary}; }
  .fg-send-btn.fg-send-stop:hover:not(:disabled) { background: ${tokens.error}; filter: brightness(1.06); }
  .fg-send-btn.fg-send-wait { background: ${tokens.softFill}; }
  .fg-send-btn.fg-send-wait .fg-send-square { background: ${tokens.textPlaceholder}; }
  .fg-send-square {
    display: block;
    width: 10px;
    height: 10px;
    border-radius: 2px;
    background: currentColor;
  }

  /* 流式状态行：阶段词 + 内联光标 + 已处理时长（替代原来那颗孤立光标）。 */
  .fg-shimmer {
    background: linear-gradient(
      110deg,
      ${tokens.textSecondary} 30%,
      ${tokens.primary} 50%,
      ${tokens.textSecondary} 70%
    );
    background-size: 220% 100%;
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
    animation: fg-shimmer 1.8s linear infinite;
  }
  @keyframes fg-shimmer {
    0% { background-position: 120% 0; }
    100% { background-position: -120% 0; }
  }
  @media (prefers-reduced-motion: reduce) {
    .fg-shimmer {
      animation: none;
      background: none;
      color: ${tokens.textSecondary};
      -webkit-text-fill-color: currentColor;
    }
  }

  /* ── 对话流：身份头 / 过程行 / 工具行 / 整轮折叠 ───────────────── */

  .fg-turn {
    padding: 16px 0;
    min-width: 0;
    position: relative;
  }
  .fg-turn-flash {
    border-radius: 12px;
    box-shadow: 0 0 0 2px ${tokens.primary};
  }

  .fg-turn-user {
    display: flex;
    justify-content: flex-end;
    padding-bottom: 8px;
    position: relative;
  }
  .fg-user-bubble {
    width: fit-content;
    max-width: calc(100% - 32px);
    background: ${tokens.softFill};
    color: ${tokens.text};
    border-radius: 14px 14px 4px 14px;
    padding: 10px 14px;
    font-size: 14.5px;
    line-height: 1.65;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    min-width: 0;
  }
  .fg-user-actions {
    position: absolute;
    right: 0;
    top: -24px;
    opacity: 0;
    transition: opacity 0.14s ease;
  }
  .fg-turn-user:hover .fg-user-actions { opacity: 1; }

  .fg-agent-head {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 12px;
    min-width: 0;
  }
  .fg-turn-live .fg-agent-head { margin-bottom: 8px; }
  .fg-agent-avatar {
    width: 24px;
    height: 24px;
    border-radius: 7px;
    flex: none;
    display: inline-grid;
    place-items: center;
    background: ${tokens.accent};
    color: ${tokens.onPrimary};
    box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.14);
  }
  .fg-agent-avatar svg {
    width: 14px;
    height: 14px;
    display: block;
    fill: currentColor;
  }
  .fg-agent-name {
    font-size: 14px;
    font-weight: 600;
    line-height: 1.75;
    color: ${tokens.text};
    white-space: nowrap;
  }
  .fg-agent-dot { color: ${tokens.primary}; }
  .fg-agent-ai {
    font-size: 9.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
    line-height: 14px;
    color: ${tokens.textPlaceholder};
    border: 1px solid ${tokens.borderStrong};
    border-radius: 4px;
    padding: 0 4px;
  }
  .fg-agent-status,
  .fg-agent-completion {
    width: max-content;
    max-width: 100%;
    color: ${tokens.textSecondary};
    font-size: 15px;
    line-height: 1.75;
    margin-bottom: 12px;
  }
  .fg-turn-copy {
    margin-left: auto;
    opacity: 0;
    transition: opacity 0.14s ease;
  }
  .fg-turn:hover .fg-turn-copy { opacity: 1; }

  .fg-answer {
    color: ${tokens.text};
    font-size: 14px;
    line-height: 1.75;
    min-width: 0;
  }
  .fg-turn > .fg-answer { margin: 0 0 12px; }
  .fg-turn > .fg-answer:last-child { margin-bottom: 0; }
  .fg-answer p { margin: 0 0 12px; }
  .fg-answer p:last-child { margin-bottom: 0; }
  .fg-answer strong { font-weight: 600; }
  .fg-answer ol,
  .fg-answer ul { margin: 0 0 12px; padding-left: 1.45em; }
  .fg-answer li { margin-bottom: 4px; }
  .fg-answer li:last-child { margin-bottom: 0; }
  .fg-answer code {
    font-family: ${tokens.fontMono};
    font-size: 0.92em;
    background: ${tokens.softFill};
    border-radius: 4px;
    padding: 1px 4px;
  }

  .fg-process-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }

  /* 通用折叠：completed / 过程消息 / 语义摘要。箭头在 completed 常显，其余 hover 才出现。 */
  .fg-fold {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .fg-fold-head {
    width: fit-content;
    max-width: 100%;
    border: 0;
    background: transparent;
    padding: 0;
    margin: 0;
    cursor: pointer;
    text-align: left;
    display: flex;
    align-items: center;
    gap: 8px;
    color: ${tokens.textSecondary};
    font-size: 15px;
    line-height: 1.75;
    transition: color 0.18s ease;
  }
  .fg-fold-head:hover { color: ${tokens.text}; }
  .fg-fold-title {
    display: block;
    min-width: 0;
    max-width: 520px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .fg-fold-lead {
    width: 14px;
    height: 14px;
    flex: none;
    display: inline-grid;
    place-items: center;
    color: ${tokens.textPlaceholder};
  }
  .fg-fold-lead svg { width: 14px; height: 14px; }
  .fg-fold-chevron {
    width: 12px;
    height: 12px;
    flex: none;
    margin-left: -4px;
    color: ${tokens.textPlaceholder};
    opacity: 0;
    transform: rotate(-90deg);
    transform-origin: 50%;
    transition: transform 0.24s cubic-bezier(0.2, 0.8, 0.2, 1), opacity 0.18s ease, color 0.18s ease;
  }
  .fg-fold-head:hover .fg-fold-chevron { opacity: 1; }
  .fg-fold.fg-open > .fg-fold-head .fg-fold-chevron { opacity: 1; transform: rotate(0deg); }
  .fg-fold-completed > .fg-fold-head .fg-fold-chevron { opacity: 1; }
  .fg-fold-body {
    display: grid;
    grid-template-rows: 0fr;
    transition: grid-template-rows 0.28s cubic-bezier(0.33, 1, 0.68, 1);
  }
  .fg-fold.fg-open > .fg-fold-body { grid-template-rows: 1fr; }
  .fg-fold-inner { min-height: 0; overflow: hidden; }
  .fg-fold.fg-open > .fg-fold-body > .fg-fold-inner { overflow: visible; min-height: auto; }
  .fg-fold-completed > .fg-fold-body > .fg-fold-inner,
  .fg-fold-process > .fg-fold-body > .fg-fold-inner,
  .fg-fold-summary > .fg-fold-body > .fg-fold-inner {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding-block: 2px;
  }

  /* ── 单行工具：图标 + 状态 + 对象 + 行号；详情按需展开 ───────── */
  /* 执行步骤 = 盒式卡（原型 §273 的 .step）：hairline 边框 + 11px 圆角 + 白底，头行 + 可折叠详情。 */
  .fg-tool {
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
    border: 1px solid ${tokens.border};
    border-radius: 11px;
    background: ${tokens.surface};
  }
  .fg-tool-head {
    width: 100%;
    min-height: 25px;
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 9px 13px;
    border: 0;
    background: transparent;
    text-align: left;
    color: ${tokens.textSecondary};
    font-size: 12.5px;
    line-height: 1.6;
    cursor: default;
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
  }
  .fg-tool-head:disabled { opacity: 1; }
  .fg-tool-expandable { cursor: pointer; }
  .fg-tool-expandable:hover,
  .fg-tool-expandable:hover .fg-tool-icon,
  .fg-tool-expandable:hover .fg-tool-status,
  .fg-tool-expandable:hover .fg-tool-primary,
  .fg-tool-expandable:hover .fg-tool-secondary { color: ${tokens.text}; }
  .fg-tool-icon {
    width: 14px;
    height: 14px;
    flex: none;
    display: inline-grid;
    place-items: center;
    color: ${tokens.textPlaceholder};
    transition: color 0.16s ease;
  }
  .fg-tool-icon svg { width: 14px; height: 14px; }
  .fg-tool-status {
    flex: none;
    white-space: nowrap;
    transition: color 0.16s ease;
  }
  .fg-tool-primary {
    min-width: 0;
    flex: 0 1 auto;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    transition: color 0.16s ease;
  }
  /* 弹性占位：让 primary 只在真的放不下时收缩（省略号），短行时又不会莫名其妙变窄。 */
  .fg-tool-spacer { flex: 1 1 auto; min-width: 0; }
  .fg-tool-link { color: ${tokens.primary}; }
  .fg-tool-expandable:hover .fg-tool-link { color: ${tokens.primaryHover}; text-decoration: underline; }
  .fg-tool-secondary {
    flex: none;
    color: ${tokens.textPlaceholder};
    font-size: 12px;
    white-space: nowrap;
  }
  .fg-tool-arrow {
    width: 16px;
    height: 16px;
    flex: none;
    margin-left: 4px;
    color: ${tokens.textPlaceholder};
    opacity: 0;
    transform: rotate(-90deg);
    transition: transform 0.24s ease, opacity 0.18s ease, color 0.18s ease;
  }
  .fg-tool-expandable:hover .fg-tool-arrow { opacity: 1; }
  .fg-tool.fg-open .fg-tool-arrow { opacity: 1; transform: rotate(0deg); }
  .fg-tool-body {
    display: grid;
    grid-template-rows: 0fr;
    transition: grid-template-rows 0.28s cubic-bezier(0.33, 1, 0.68, 1);
  }
  .fg-tool.fg-open .fg-tool-body { grid-template-rows: 1fr; }
  .fg-tool-body-inner { min-height: 0; overflow: hidden; }
  .fg-tool.fg-open .fg-tool-body > .fg-tool-body-inner { min-height: auto; overflow: visible; }
  .fg-tool-detail {
    background: ${tokens.softFill};
    border-radius: 8px;
    padding: 10px 13px;
    margin: 0 13px 12px;
    max-height: 420px;
    overflow: auto;
    display: flex;
    flex-direction: column;
    gap: 6px;
    color: ${tokens.textSecondary};
    font-size: 12px;
  }
  .fg-tool-detail-kicker {
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    color: ${tokens.textPlaceholder};
    text-transform: lowercase;
  }
  .fg-tool-detail pre {
    margin: 0;
    font-family: ${tokens.fontMono};
    font-size: 12px;
    line-height: 1.65;
    color: ${tokens.text};
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .fg-tool-detail-error {
    color: ${tokens.error};
    font-size: 12px;
    line-height: 1.6;
    white-space: pre-wrap;
  }
  .fg-tool-detail-meta {
    color: ${tokens.textPlaceholder};
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
  }
  .fg-tool-detail-path {
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    line-height: 1.55;
    color: ${tokens.textSecondary};
    overflow-wrap: anywhere;
  }

  /* read/list：带行号的文件预览（只在调用参数给了起始行时编号）。 */
  .fg-tool-code {
    border: 1px solid ${tokens.border};
    border-radius: 8px;
    background: ${tokens.codeBg};
    overflow: auto;
    max-height: 260px;
    padding: 6px 0;
  }
  .fg-tool-code-line {
    display: flex;
    gap: 10px;
    padding: 0 10px;
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    line-height: 1.6;
    white-space: pre;
  }
  .fg-tool-line-no {
    flex: none;
    min-width: 3.2ch;
    text-align: right;
    color: ${tokens.textPlaceholder};
    user-select: none;
  }
  .fg-tool-code-text { color: ${tokens.text}; }

  /* edit/write：diff 行（+/−/上下文），数据不足时退回结果摘要。 */
  .fg-tool-diff {
    border: 1px solid ${tokens.border};
    border-radius: 8px;
    background: ${tokens.codeBg};
    overflow: auto;
    max-height: 280px;
    padding: 6px 0;
  }
  .fg-diff-line {
    display: flex;
    gap: 8px;
    padding: 0 10px;
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    line-height: 1.6;
    white-space: pre;
    color: ${tokens.text};
  }
  .fg-diff-sign {
    flex: none;
    width: 1ch;
    color: ${tokens.textPlaceholder};
    user-select: none;
  }
  .fg-diff-add { background: ${tokens.success}14; }
  .fg-diff-add .fg-diff-sign { color: ${tokens.success}; }
  .fg-diff-del { background: ${tokens.error}14; }
  .fg-diff-del .fg-diff-sign { color: ${tokens.error}; }
  .fg-diff-meta,
  .fg-diff-context { color: ${tokens.textSecondary}; }
  .fg-diff-meta { color: ${tokens.textPlaceholder}; }

  /* execute：命令行 + stdout/stderr + 退出码。 */
  .fg-tool-command {
    display: flex;
    gap: 8px;
    padding: 6px 10px;
    border: 1px solid ${tokens.border};
    border-radius: 8px;
    background: ${tokens.codeBg};
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    line-height: 1.6;
    color: ${tokens.text};
    overflow-wrap: anywhere;
  }
  .fg-tool-command-sign {
    flex: none;
    color: ${tokens.textPlaceholder};
    user-select: none;
  }
  .fg-tool-command-text { white-space: pre-wrap; }
  .fg-tool-output {
    border: 1px solid ${tokens.border};
    border-radius: 8px;
    background: ${tokens.codeBg};
    padding: 8px 10px;
    max-height: 240px;
    overflow: auto;
  }
  .fg-tool-output-err { color: ${tokens.error}; }
  .fg-tool-exit {
    font-family: ${tokens.fontMono};
    font-size: 11px;
    color: ${tokens.textSecondary};
  }
  .fg-tool-exit-bad { color: ${tokens.error}; }

  /* search/web：查询词 + 结果列表。 */
  .fg-tool-query {
    font-family: ${tokens.fontMono};
    font-size: 11.5px;
    line-height: 1.6;
    color: ${tokens.text};
    overflow-wrap: anywhere;
  }
  .fg-tool-results {
    margin: 0;
    padding-left: 1.4em;
    display: flex;
    flex-direction: column;
    gap: 4px;
    color: ${tokens.text};
    font-size: 12px;
    line-height: 1.6;
    overflow-wrap: anywhere;
  }
  .fg-tool-note {
    color: ${tokens.textPlaceholder};
    font-size: 11px;
    line-height: 1.55;
  }

  /* 轮内过程块：搜索锚点用 data-fg-idx 定位，这里只负责布局。 */
  .fg-process-item { min-width: 0; }

  /* 流式尾部：阶段词 + 内联光标（与 turn-status 区分：这里是过程流末尾）。 */
  .fg-turn-footer {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 4px;
    width: max-content;
    max-width: 100%;
    color: ${tokens.textSecondary};
    font-size: 15px;
    line-height: 1.75;
  }
  .fg-turn-footer-caret {
    width: 2px;
    height: 17px;
    flex: none;
    border-radius: 1px;
    background: ${tokens.textPlaceholder};
    animation: fg-blink 1.1s steps(1) infinite;
  }
  .fg-turn-usage {
    display: flex;
    align-items: center;
    gap: 4px;
    margin-top: 8px;
    color: ${tokens.textPlaceholder};
    font-family: ${tokens.fontMono};
    font-size: 10.5px;
    cursor: help;
  }
  .fg-turn-usage-sep { color: ${tokens.border}; }

  /* 思考折叠块（需求 6 下半）：15px 标题；流式中 shimmer 且无箭头；正文 13/20、左 4px 细线。 */
  .fg-thought-head {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 2px 0;
    border: 0;
    background: transparent;
    font: inherit;
    font-size: 15px;
    color: ${tokens.textSecondary};
    line-height: 1.75;
    text-align: left;
    cursor: pointer;
  }
  .fg-thought-head:hover:not(:disabled) { color: ${tokens.text}; }
  .fg-thought-head:disabled { cursor: default; }
  .fg-thought-chevron {
    flex: none;
    color: ${tokens.textPlaceholder};
    opacity: 0;
    transition: transform 0.2s ease, opacity 0.16s ease;
  }
  .fg-thought-head:hover .fg-thought-chevron { opacity: 1; }
  .fg-thought-chevron.fg-open { opacity: 1; transform: rotate(90deg); }
  .fg-thought-clip {
    overflow: hidden;
    min-width: 0;
    transition: max-height 0.28s cubic-bezier(0.33, 1, 0.68, 1), opacity 0.18s ease;
  }
  .fg-thought-body {
    max-height: 200px;
    overflow-y: auto;
    /* 只在块内纵向滚动；不写 overflow-x 时浏览器会把另一轴算成 auto，横向被长行撑开后冒横条。 */
    overflow-x: hidden;
    scrollbar-width: thin;
    border-left: 4px solid ${tokens.border};
    margin: 8px 0 4px;
    padding: 0 0 0 12px;
    font-size: 13px;
    line-height: 20px;
    color: ${tokens.text};
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    min-width: 0;
    transform: translateY(-6px);
    transition: transform 0.28s cubic-bezier(0.33, 1, 0.68, 1);
  }
  .fg-thought-clip[data-open='true'] .fg-thought-body { transform: translateY(0); }
  @media (prefers-reduced-motion: reduce) {
    .fg-thought-clip,
    .fg-thought-body { transition: none; }
    .fg-thought-body { transform: none; }
  }

  /* ——— 全站按钮口径（不只在工具行：行内「编辑 / 删除 / 上传」也走这套）———
     ① antd 会给**两个中文字**的按钮强插 letter-spacing:0.34em 再用负 margin 补偿，
        结果是「编 辑」「删 除」「新 建」被拉散。全站一律抹平。
     ② 文字推独立合成层：flex 居中的半像素文字上屏前按整像素光栅，消除发虚。 */
  .ant-btn.ant-btn-two-chinese-chars::first-letter,
  .ant-btn.ant-btn-two-chinese-chars > *:not(.anticon) {
    letter-spacing: normal;
    margin-inline-end: 0;
  }
  .ant-btn > span { transform: translateZ(0); }

  /* ——— 危险按钮（删除 / 撤销）：静息态不喊 ———
     antd 的危险按钮天生是"红字 + 红边 + 红晕投影"，一排动作里最抢眼的那块颜色给了最该被谨慎点击的
     那一颗；这里把它拉回中性边（与次按钮同族），只有 hover / active 才染红——危险靠颜色语义提示，
     不靠常驻的红框。红晕投影已在 theme.ts 的 dangerShadow 里撤掉。 */
  .ant-btn.ant-btn-dangerous.ant-btn-default:not(:disabled) {
    border-color: ${tokens.borderStrong};
    background: ${tokens.surface};
    color: ${tokens.error};
  }
  .ant-btn.ant-btn-dangerous.ant-btn-default:not(:disabled):hover {
    border-color: ${tokens.dangerBorder};
    background: ${tokens.dangerSoft};
    color: ${tokens.error};
  }
  .ant-btn.ant-btn-dangerous.ant-btn-default:not(:disabled):active {
    border-color: ${tokens.error};
    background: ${tokens.dangerSoft};
    color: ${tokens.error};
  }

  /* 全站可见焦点环（键盘可达性）：antd 的默认 outline 偏细，这里统一成一档品牌环。 */
  .ant-btn:focus-visible,
  .fg-chip:focus-visible,
  .fg-cap-add:focus-visible,
  .fg-cap-chip:focus-visible,
  .fg-model-pill:focus-visible,
  .fg-ws-pill:focus-visible {
    outline: 2px solid ${tokens.accent};
    outline-offset: 2px;
  }

  /* ——— 工具行（SectionHeader.tools / CardToolbar）：控件等高、同族 ——— */
  .fg-toolbar { display: inline-flex; align-items: center; gap: 8px; }
  .fg-toolbar .ant-btn,
  .fg-toolbar .ant-input-affix-wrapper,
  .fg-toolbar .ant-input {
    height: 32px;
    border-radius: ${tokens.radiusSm}px;
    box-sizing: border-box;
  }
  /* 图标按钮与文字按钮同宽高比，字重走真实档 600（560 无字形会合成发虚） */
  .fg-toolbar .ant-btn { padding-inline: 13px; font-weight: 600; }
  .fg-toolbar .ant-btn-icon-only { padding-inline: 0; width: 32px; }
  .fg-toolbar .ant-btn .anticon { font-size: 14px; }
  .fg-toolbar .ant-input-affix-wrapper .ant-input { height: auto; }
  .fg-toolbar .ant-input { transform: translateZ(0); }

`

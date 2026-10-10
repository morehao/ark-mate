#!/usr/bin/env node
/**
 * Web 界面验收（T-4：M3-4 能力勾选面板 / M4-6 自动化页 / M5′ 命令补全 / v11「设置 · 模型」一节）——**真 Chrome + 真服务**。
 *
 * 为什么这里才出现浏览器驱动代码：本仓前端零测试依赖（无 vitest/jest/playwright/puppeteer），
 * 既有 `smoke-e2e.mjs` / `e2e-live-turn.mjs` 只走 REST/WS。而 M3-4 与 M4-6 的 DoD 是**手工界面
 * 验收**（勾选后建会话、preview 对照、手动 run、会话带标跳转），REST 冒烟替不了"界面真的长出来了"。
 * 于是用 Node 24 自带的全局 WebSocket 直连 CDP——**不引入任何新依赖**。
 *
 * 用法：
 *   node scripts/web-ui-acceptance.mjs                    # 默认四组（cap/cmd/auto/model）
 *   node scripts/web-ui-acceptance.mjs cap auto cmd model  # 只跑指定场景
 *   node scripts/web-ui-acceptance.mjs model               # 只跑「设置 · 模型」那一节
 *   KEEP_CHROME=1 ...                                      # 保留 Chrome 便于人工接管
 *
 * `model` 场景 2026-10-10 已在真 Chrome（headless）+ `make dev` 上跑绿（19/19），随后并进默认集合。
 *
 * 前置：后端（:10001）+ vite（:5173）已起（`make dev`）。脚本**不启动服务**——
 * 它验的是"人在看的那个界面"，偷偷另起一个服务就验错了对象。
 *
 * 环境写入（脚本自己造、自己清）：用 M2 写面建两个验收用 skill；把默认 agent 切到 opencode
 * （M3-4 与 M5′ 都要它）并在结束时切回。跑完用 `--cleanup-only` 也能单独清场。
 */

import { spawn } from 'node:child_process'
import { mkdirSync, writeFileSync, readFileSync, rmSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

// 仓库根按脚本位置定位：`pnpm ui:accept` 的 cwd 是 frontend/，用相对路径会把截图写进 frontend/.dsh/
const REPO_ROOT = fileURLToPath(new URL('../..', import.meta.url))

const WEB_URL = process.env.WEB_URL ?? 'http://localhost:5173'
const MATE_URL = process.env.MATE_URL ?? 'http://127.0.0.1:10001'
const CHROME = process.env.CHROME_BIN ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const SHOT_DIR = process.env.SHOT_DIR ?? join(REPO_ROOT, '.dsh/docs/notes/2026-09-24-panel-ui-acceptance')
const PROFILE = join(tmpdir(), `mate-ui-accept-${process.pid}`)
const SKILLS = { applicable: 'ui-verify-oc', inapplicable: 'ui-verify-dsh' }
const AUTO_ID = 'ui-verify-daily'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// 全局控制台错误：场景抛错时一并打印。**只加不减**——真出问题时"只有一句超时"是最难查的形态，
// 而控制台里的真相（403、未捕获异常、antd 警告）就在手边。
const globalPageErrors = []

// 脚本自己造出来的会话（收尾统一归档：不留垃圾在本地开发实例里）
const createdConversations = []

// ——— 断言台账 ———
const results = []
function check(name, ok, detail = '') {
  results.push({ name, ok: !!ok, detail })
  console.log(`${ok ? '  ✅' : '  ❌'} ${name}${detail ? ` — ${detail}` : ''}`)
  return !!ok
}
const section = (t) => console.log(`\n=== ${t} ===`)

// ——— 最小 CDP 客户端（浏览器级 WS + flatten session）———
function connectCDP(wsUrl) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(wsUrl)
    let id = 0
    const pending = new Map()
    const listeners = []
    ws.onmessage = (ev) => {
      const msg = JSON.parse(ev.data)
      if (msg.id != null && pending.has(msg.id)) {
        const { res, rej } = pending.get(msg.id)
        pending.delete(msg.id)
        msg.error ? rej(new Error(`${msg.error.message} (${JSON.stringify(msg.error.data ?? '')})`)) : res(msg.result)
        return
      }
      for (const l of listeners) l(msg)
    }
    ws.onerror = (e) => reject(new Error(`CDP WS 错误: ${e.message ?? e}`))
    ws.onopen = () =>
      resolve({
        ws,
        send(method, params = {}, sessionId) {
          const mid = ++id
          return new Promise((res, rej) => {
            pending.set(mid, { res, rej })
            ws.send(JSON.stringify({ id: mid, method, params, ...(sessionId ? { sessionId } : {}) }))
            setTimeout(() => {
              if (pending.has(mid)) {
                pending.delete(mid)
                rej(new Error(`CDP 超时: ${method}`))
              }
            }, 30000)
          })
        },
        on(fn) {
          listeners.push(fn)
        },
        close() {
          ws.close()
        },
      })
  })
}

async function launchChrome() {
  const portFile = join(PROFILE, 'DevToolsActivePort')
  rmSync(PROFILE, { recursive: true, force: true })
  const child = spawn(
    CHROME,
    [
      '--headless=new',
      '--remote-debugging-port=0',
      `--user-data-dir=${PROFILE}`,
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-gpu',
      // macOS + Chrome 155 的 headless=new：**不加**这个开关时，走"浏览器级 WS + 扁平会话"的
      // Page 域命令（`Page.enable`）永不回包——`Target.createTarget`/`attachToTarget` 都正常，
      // 随后就卡死（2026-10-10 在本机实测复现并定位）。它是验收脚本自己的 Chrome 实例，
      // 与用户正在用的浏览器无关，所以这里放开沙箱是安全且必要的。
      '--no-sandbox',
      '--hide-scrollbars',
      '--window-size=1512,982',
      'about:blank',
    ],
    { stdio: 'ignore' },
  )
  for (let i = 0; i < 100; i++) {
    if (existsSync(portFile)) {
      const [port] = readFileSync(portFile, 'utf8').split('\n')
      if (port) return { child, port: Number(port) }
    }
    if (child.exitCode != null) throw new Error(`Chrome 提前退出 code=${child.exitCode}`)
    await sleep(100)
  }
  throw new Error('等待 DevToolsActivePort 超时')
}
/** 打开一个页面并返回操作句柄。 */
async function openPage(browser, url) {
  const { targetId } = await browser.send('Target.createTarget', { url: 'about:blank' })
  const { sessionId } = await browser.send('Target.attachToTarget', { targetId, flatten: true })
  const send = (m, p) => browser.send(m, p, sessionId)
  const errors = []
  browser.on((msg) => {
    if (msg.sessionId !== sessionId) return
    if (msg.method === 'Runtime.consoleAPICalled' && msg.params.type === 'error') {
      errors.push((msg.params.args ?? []).map((a) => a.value ?? a.description ?? a.type).join(' '))
    }
    if (msg.method === 'Runtime.exceptionThrown') {
      const d = msg.params.exceptionDetails
      errors.push(d.exception?.description ?? d.text)
      globalPageErrors.push('EXC ' + (d.exception?.description ?? d.text))
    }
    if (msg.method === 'Log.entryAdded' && msg.params.entry.level === 'error') {
      errors.push(`${msg.params.entry.source}: ${msg.params.entry.text}`)
      globalPageErrors.push(`${msg.params.entry.source}: ${msg.params.entry.text}`)
    }
  })
  await send('Page.enable')
  await send('Runtime.enable')
  await send('Log.enable')

  const page = {
    errors,
    send,
    async evaluate(expr) {
      const r = await send('Runtime.evaluate', {
        expression: `(() => { ${expr} })()`,
        returnByValue: true,
        awaitPromise: true,
      })
      if (r.exceptionDetails) throw new Error(`evaluate 抛错: ${r.exceptionDetails.exception?.description ?? r.exceptionDetails.text}`)
      return r.result.value
    },
    async goto(u, { waitText } = {}) {
      await send('Page.navigate', { url: u })
      for (let i = 0; i < 200; i++) {
        if ((await page.evaluate('return document.readyState')) === 'complete') break
        await sleep(100)
      }
      if (waitText) await page.waitFor(textExpr(waitText), { label: `页面出现「${waitText}」` })
    },
    async waitFor(expr, { label = expr, timeout = 25000 } = {}) {
      const t0 = Date.now()
      while (Date.now() - t0 < timeout) {
        try {
          if (await page.evaluate(expr)) return true
        } catch {
          /* 导航/重渲染中途 evaluate 可能失败，继续轮询 */
        }
        await sleep(120)
      }
      throw new Error(`等待超时：${label}`)
    },
    async box(expr) {
      return page.evaluate(`
        const el = ${expr};
        if (!el) return null;
        const r = el.getBoundingClientRect();
        if (r.width === 0 && r.height === 0) return null;
        return { x: r.x + r.width / 2, y: r.y + r.height / 2, w: r.width, h: r.height,
                 text: (el.innerText || el.textContent || '').trim().slice(0, 60) };
      `)
    },
    /** 把元素滚进视口（长弹窗里 Prompt/工作空间在可视区外，坐标点击会落空）。 */
    async scrollTo(expr) {
      await page.evaluate(`const el = ${expr}; if (el && el.scrollIntoView) el.scrollIntoView({ block: 'center' }); return true`)
      await sleep(120)
    },
    /** 真实鼠标点击（antd 的 Popover/Select/Segmented 认 mousedown/mouseup，合成 click 不够）。 */
    async click(expr, { optional = false } = {}) {
      await page.scrollTo(expr)
      const b = await page.box(expr)
      if (!b) {
        if (optional) return false
        throw new Error(`点不到元素: ${expr}`)
      }
      const p = { x: Math.round(b.x), y: Math.round(b.y), button: 'left', clickCount: 1 }
      await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: p.x, y: p.y })
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', ...p })
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...p })
      return true
    },
    /** 聚焦元素后用真实输入管线插入文本（React 受控组件能收到 onChange）。 */
    async type(expr, text) {
      await page.click(expr)
      await sleep(80)
      await send('Input.insertText', { text })
      await sleep(150)
    },
    async press(key) {
      const codes = { Enter: 13, Escape: 27, Tab: 9, ArrowDown: 40, ArrowUp: 38 }
      const code = codes[key]
      for (const type of ['keyDown', 'keyUp']) {
        await send('Input.dispatchKeyEvent', { type, key, windowsVirtualKeyCode: code, nativeVirtualKeyCode: code })
      }
      await sleep(200)
    },
    /** antd Select：点开 → 在搜索框输入 → 回车选中。 */
    async pickSelect(selectExpr, optionText, { searchable = true } = {}) {
      await page.click(selectExpr)
      await sleep(250)
      if (searchable) {
        await send('Input.insertText', { text: optionText })
        await sleep(350)
      }
      // antd v6 改了类名（选中项是 .ant-select-content，不再是 .ant-select-selection-item），
      // 这里按"包含 ant-select-item 的可见项"取，别写死版本相关的类名。
      const opt = `[...document.querySelectorAll('[class*="ant-select-dropdown"]:not([class*="hidden"]) [class*="ant-select-item"]')].find(o => (o.innerText||'').includes(${JSON.stringify(optionText)}))`
      await page.click(opt)
      await sleep(200)
    },
    text: () => page.evaluate('return document.body.innerText'),
    count: (expr) => page.evaluate(`return document.querySelectorAll(${JSON.stringify(expr)}).length`),
    async shot(name) {
      const { data } = await send('Page.captureScreenshot', { format: 'png' })
      mkdirSync(SHOT_DIR, { recursive: true })
      const file = join(SHOT_DIR, `${name}.png`)
      writeFileSync(file, Buffer.from(data, 'base64'))
      console.log(`  📷 ${file}`)
      return file
    },
  }
  await page.goto(url)
  return page
}

// ——— 选择器小工具 ———
/** 用 JS 触发 click（元素滚出可视区或被遮挡时用；antd 按钮认合成 click）。 */
const clickJS = (page, expr) =>
  page.evaluate(`const el = ${expr}; if (!el) return false; el.click(); return true`)

/** 直接发一条 CDP 输入事件（hover 等）。 */
const send2 = (page, params) => page.send('Input.dispatchMouseEvent', params)

/**
 * 按文本找按钮的 JS 表达式（**忽略空白**）。
 *
 * 为什么不能直接 `innerText.trim() === '保存'`：antd 默认 `autoInsertSpace` 会在**两个中文之间**
 * 插一个空格，于是按钮的实际文本是「保 存」——按逐字比较永远匹配不上（J9 场景首次实跑就撞上）。
 */
function btnExpr(text) {
  return `[...document.querySelectorAll('button')].find(b => (b.innerText||'').replace(/\\s+/g,'') === ${JSON.stringify(text)})`
}

/**
 * 取**包含某个字段**的那个弹窗里的按钮（按文本、忽略空白）。
 *
 * 为什么不能直接 `[...document.querySelectorAll('button')].find(...)`：antd 的**嵌套弹窗不会卸载**，
 * 关掉的那一层仍在 DOM 里——于是"第一个文本是「保存」的按钮"很可能是**上一层隐藏弹窗**里的那个。
 * 首次实跑就撞上：点「保存」把**供应商弹窗**又提交了一遍，而模型那条压根没发请求（表现是
 * "表单填得好好的、保存后什么都没有、toast 也是空的"）。按字段所在的弹窗限定作用域即可。
 */
function saveBtnExpr(fieldID) {
  return `(() => {
    const inp = document.querySelector('.ant-modal input#${fieldID}');
    const modal = inp && inp.closest('.ant-modal');
    return modal ? [...modal.querySelectorAll('button')].find(b => (b.innerText||'').replace(/\\s+/g,'') === '保存') : null
  })()`
}

/**
 * 选中一个 antd Select 的某项（按**表单字段的 input id** 定位）。
 *
 * 三条路径依次尝试，每条都真实走输入管线：① 真实鼠标点下拉项；② 直接回车（showSearch 下
 * 过滤后的首项即目标）；③ ↓ + 回车（把激活项挪到第一项）。
 *
 * 为什么要这三步：**首次实跑**里合成 `el.click()`、`.trim() === '保存'`、`.ant-modal .ant-select`
 * 三种"看起来显然"的写法全是坑——antd 会插空格、嵌套弹窗里 DOM 顺序会先命中外层控件、
 * 合成 click 对 Select 选项不稳。这里把踩过的坑收敛成一个助手，调用点只写"选哪一项"。
 */
async function selectByFieldID(page, fieldID, text) {
  const inputSel = `.ant-modal input#${fieldID}`
  const shown = async () => page.evaluate(
    `return (document.querySelector(${JSON.stringify(inputSel)})?.closest('.ant-select')?.innerText || '').includes(${JSON.stringify(text)})`,
  )
  await page.click(`document.querySelector(${JSON.stringify(inputSel)})`)
  await sleep(300)
  await page.send('Input.insertText', { text })
  await sleep(400)
  const optExpr = `[...document.querySelectorAll('[class*="ant-select-dropdown"]:not([class*="hidden"]) [class*="ant-select-item"]')]
     .find(o => (o.innerText||'').includes(${JSON.stringify(text)}))`
  if (await page.box(optExpr)) {
    await page.click(optExpr)
    await sleep(250)
  }
  if (!(await shown())) {
    await page.press('Enter')
    await sleep(250)
  }
  if (!(await shown())) {
    await page.press('ArrowDown')
    await page.press('Enter')
    await sleep(250)
  }
  return { ok: await shown(), detail: await page.evaluate(`
    const el = document.querySelector(${JSON.stringify(inputSel)});
    return JSON.stringify({
      found: !!el,
      selectText: el ? (el.closest('.ant-select')?.innerText || '').replace(/\\n/g,'|') : null,
      items: [...document.querySelectorAll('[class*="ant-select-dropdown"]:not([class*="hidden"]) [class*="ant-select-item"]')].map((i) => (i.innerText||'').trim()),
    })`) }
}

/** 按可见文本找元素（默认包含匹配，`exact=true` 时全等）。 */
function byText(sel, text, exact = false) {
  const cmp = exact
    ? `(el.innerText||el.textContent||'').trim() === ${JSON.stringify(text)}`
    : `(el.innerText||el.textContent||'').includes(${JSON.stringify(text)})`
  return `[...document.querySelectorAll(${JSON.stringify(sel)})].find(el => ${cmp})`
}
const textExpr = (t) => `return (document.body.innerText||"").includes(${JSON.stringify(t)})`
/** 页面内 querySelector 表达式（page.click/type 要的是 JS 表达式，不是裸 CSS 选择器）。 */
const q = (css) => `document.querySelector(${JSON.stringify(css)})`

// ——— REST 辅助（交叉验证：界面看到的在 API 上也成立）———
/**
 * 轮询一个会话直到离开 running/pending（返回终态；超时返回 'timeout'）。
 *
 * 为什么要它：自动化触发的会话，其来源标只在列表被刷新后才出现，而列表刷新由
 * "终态事件 → refreshConversations" 驱动——固定 sleep 等于把断言押在模型速度上。
 */
async function waitConversationTerminal(id, timeoutMs = 90000) {
  // 终态闭集取自前端契约的 `ConversationStatus`（`in_progress` = 有轮次在跑，**不是**终态）。
  const TERMINAL = new Set(['completed', 'cancelled', 'failed', 'interrupted'])
  const t0 = Date.now()
  let last = 'unknown'
  while (Date.now() - t0 < timeoutMs) {
    try {
      // api() 返回的是 { status, json }（信封在 json 里）
      const r = await api('GET', `/conversations/${id}`)
      last = r?.json?.data?.status ?? last
      if (TERMINAL.has(last)) return last
    } catch {
      // 暂时读不到（刚创建）：继续等
    }
    await sleep(1000)
  }
  return `timeout(${last})`
}

async function api(method, path, body) {
  const res = await fetch(`${MATE_URL}${path.startsWith('/v1') ? path : `/v1/mate${path}`}`, {
    method,
    headers: body ? { 'content-type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  try {
    return { status: res.status, json: JSON.parse(text) }
  } catch {
    throw new Error(`${method} ${path} 返回非 JSON(${res.status}): ${text.slice(0, 200)}`)
  }
}

/** 造验收用 skill（幂等：40901 存在即算成功）。 */
async function seedSkills() {
  for (const [id, runtimes, name] of [
    [SKILLS.applicable, ['opencode'], 'UI验收-仅opencode'],
    [SKILLS.inapplicable, ['dsh'], 'UI验收-仅dsh'],
  ]) {
    const r = await api('POST', '/skills', {
      id,
      name,
      description: '界面验收用（脚本自建自清）',
      body: '这是一条界面验收用的技能说明，不含任何危险指令。',
      runtimes,
    })
    if (r.json.code !== 0 && !String(r.json.msg ?? '').includes('已存在')) {
      throw new Error(`建 skill ${id} 失败: ${JSON.stringify(r.json)}`)
    }
  }
}

async function cleanupCreated() {
  for (const id of createdConversations) {
    const r = await api('PATCH', `/conversations/${id}`, { archived: true }).catch(() => null)
    console.log(`  🧹 归档验收会话 ${id}: ${r ? r.json.code : 'n/a'}`)
  }
}

async function cleanupSkills() {
  for (const id of Object.values(SKILLS)) {
    const r = await api('DELETE', `/skills/${id}`).catch(() => null)
    console.log(`  🧹 清理 skill ${id}: ${r ? r.json.code : 'n/a'}`)
  }
  await api('DELETE', `/automations/${AUTO_ID}`).catch(() => null)
}

/** 切默认 agent（M3-4 的清单按 agent 视角取；结束时切回）。 */
async function setDefaultAgent(id) {
  const r = await api('POST', `/agents/${id}/default`)
  if (r.json.code !== 0 && !String(r.json.msg ?? '').includes('已')) {
    // 端点形态兜底：改 config 的 default_agent
    console.log(`  (set default via /agents/${id}/default → ${JSON.stringify(r.json).slice(0, 120)})`)
  }
}

// ————————————————————————————————————————————————————————————————
// 场景一：M3-4 能力勾选面板
// DoD（计划 §5）：勾选后建会话 → `GET /conversations/:id` 回显选定明细与钉住的指纹；
//                不可达时前端给解释。
// ————————————————————————————————————————————————————————————————
async function scenarioCapability(browser) {
  section('M3-4 能力勾选面板（勾选 skill 子集 → 建会话 → 回显选定与指纹）')
  const page = await openPage(browser, `${WEB_URL}/agents`)
  await page.waitFor(textExpr('设为当前'), { label: 'Agents 页渲染' })
  await page.shot('01-agents')
  // 切默认 agent 到 opencode（清单里有"仅 opencode 适用"与"仅 dsh 适用"两条，才能验不可达解释）
  const setBtn = `[...document.querySelectorAll('button')].filter(b => (b.innerText||'').includes('设为当前'))`
  const n = await page.evaluate(`return ${setBtn}.length`)
  check('Agents 页有且仅有一个非默认 agent 的「设为当前」', n === 1, `按钮数=${n}`)
  await page.click(`${setBtn}[0]`)
  await page.waitFor(`return ${JSON.stringify(true)}`, { label: '等待切换落定', timeout: 3000 }).catch(() => {})
  await sleep(1200)
  const def = await api('GET', '/agents')
  const opencodeDefault = (def.json.data ?? []).find((a) => a.id === 'opencode')?.is_default === true
  check('默认 agent 已切到 opencode（界面操作生效）', opencodeDefault, `agents=${(def.json.data ?? []).map((a) => `${a.id}:${a.is_default}`).join(' ')}`)

  // 回新建页，打开「可用的能力」
  await page.goto(`${WEB_URL}/`, { waitText: '可用的能力' })
  await page.waitFor(textExpr('opencode'), { label: '新建卡显示当前 Agent=opencode' })
  await page.shot('02-start-with-opencode')
  const pill = byText('button', '能力 ·')
  await page.click(pill)
  await page.waitFor(`const el = document.querySelector('.fg-cap-pop'); return !!el && el.getClientRects().length > 0`, { label: '能力弹层打开' })
  await sleep(500)
  await page.shot('03-capability-popover')
  const popDefault = await page.evaluate(`return document.querySelector('.fg-cap-pop').innerText`)
  check('缺省态不展开清单，只写"按 Agent 声明投放（当前 N 个）"', /按 Agent 声明投放（当前 \d+ 个）/.test(popDefault), popDefault.split('\n').slice(0, 5).join(' | '))

  // 技能区切「自定义」→ 预填默认集 → 列表才展开（三态里的"自定义"分支）
  await page.click(`[...document.querySelectorAll('.fg-cap-mode-btn')].find(b => b.innerText.trim() === '自定义')`)
  await sleep(400)
  await page.shot('04-capability-custom')
  const custom = await page.evaluate(`return document.querySelector('.fg-cap-pop').innerText`)
  check('切自定义后给出"只投勾选的 N 个"与全选/清空', /只投勾选的/.test(custom) && /全选/.test(custom), custom.split('\n').filter((l) => /只投|全选/.test(l)).join(' | '))
  check('自定义列表列出适用 skill', custom.includes('UI验收-仅opencode'), custom.split('\n').filter((l) => l.includes('UI验收')).join(' | ').slice(0, 200))
  check('不适用 skill 也列出来（不隐藏）', /UI验收-仅dsh/.test(custom), custom.split('\n').filter((l) => l.includes('dsh')).join(' | ').slice(0, 240))
  check('不适用项标「不可选」且带 fg-off', (await page.count('.fg-cap-opt.fg-off')) >= 1 && custom.includes('不可选'), `fg-off 行数=${await page.count('.fg-cap-opt.fg-off')} 含不可选=${custom.includes('不可选')}`)
  // 原因在 tooltip 里：真的 hover 一次，确认"就地给原因"不是写着好看
  const whyExpr = `[...document.querySelectorAll('.fg-cap-opt.fg-off .fg-cap-opt-why')][0]`
  if (await page.box(whyExpr)) {
    // 先把指针挪开再进目标：否则"指针本来就在那儿"不会触发 mouseenter
    await send2(page, { type: 'mouseMoved', x: 5, y: 400 })
    await sleep(200)
    const b = await page.box(whyExpr)
    await send2(page, { type: 'mouseMoved', x: Math.round(b.x), y: Math.round(b.y) })
    // 精确指认"这一条"提示：hover 后 antd 把弹层 id 写进 aria-describedby
    const tipExpr = `(() => { const el = ${whyExpr}; const id = el && el.getAttribute('aria-describedby'); const n = id && document.getElementById(id); return n ? n.innerText : '' })()`
    await page.waitFor(`return ${tipExpr}.length > 0`, { label: 'tooltip 出现', timeout: 6000 }).catch(() => {})
    await sleep(300)
    await page.shot('04b-capability-why')
    const tip = await page.evaluate(`return ${tipExpr}`)
    if (process.env.DEBUG_HOVER) {
      console.log('  [debug] why outerHTML =', await page.evaluate(`return ${whyExpr}.outerHTML`))
      console.log('  [debug] 全 body 里含不相交的节点 =', await page.evaluate(`return [...document.querySelectorAll('*')].filter(e => (e.textContent||'').includes('不相交') && e.children.length === 0).map(e => e.className + ' :: ' + e.textContent.slice(0,140)).join(' || ').slice(0,500)`))
      console.log('  [debug] 可见 popover =', await page.evaluate(`return !!document.querySelector('.ant-popover:not(.ant-popover-hidden) .fg-cap-pop')`))
      console.log('  [debug] why 的 rect =', JSON.stringify(await page.box(whyExpr)))
    }
    check('hover 不适用项给出原因（tooltip）', /runtimes|不相交|dsh/.test(tip), tip.slice(0, 200))
  } else {
    check('hover 不适用项给出原因（tooltip）', false, '没有 .fg-cap-opt-why 元素')
  }

  // 真正勾一下（三态语义：一个都不勾 = 明确一个都不要；再勾回来 = 只剩它）
  const ocCheckbox = `[...document.querySelectorAll('.fg-cap-opt')].find(o => (o.innerText||'').includes('UI验收-仅opencode'))`
  await page.click(`${ocCheckbox}.querySelector('input[type=checkbox]')`)
  await sleep(350)
  const noneSel = await page.evaluate(`return document.querySelector('.fg-cap-pop').innerText`)
  check('取消勾选 → 明确表达"一个都不要"', /一个都不要/.test(noneSel), noneSel.split('\n').filter((l) => /只投/.test(l)).join(' | '))
  await page.click(`${ocCheckbox}.querySelector('input[type=checkbox]')`)
  await sleep(350)
  const oneSel = await page.evaluate(`return document.querySelector('.fg-cap-pop').innerText`)
  check('重新勾选 → 只投勾选的 1 个', /只投勾选的 1 个/.test(oneSel), oneSel.split('\n').filter((l) => /只投/.test(l)).join(' | '))
  await page.shot('05-capability-selected')

  // 关掉弹层：它盖在输入框上方，不关就会把后面的点击/输入吃掉（第一版就是这么失败的）
  // 点弹层外的空白关掉（Popover 认"外部点击"；Esc 在 antd 的受控 Popover 上不生效，
  // 而标题 h1 正好被弹层盖住——第一次这么点等于点在弹层里）
  await send2(page, { type: 'mouseMoved', x: 400, y: 872 })
  await send2(page, { type: 'mousePressed', x: 400, y: 872, button: 'left', clickCount: 1 })
  await send2(page, { type: 'mouseReleased', x: 400, y: 872, button: 'left', clickCount: 1 })
  await page.waitFor(
    `const el = document.querySelector('.fg-cap-pop'); return !el || el.getClientRects().length === 0`,
    { label: '能力弹层关闭（不可见即可，antd 关闭后 DOM 仍留）' },
  )

  // 建会话（发一句话）：Enter 发送（与界面提示行一致）
  await page.type(q('textarea'), '只回复 ok 两个字，不要调用任何工具。')
  await page.shot('06-before-send')
  check('草稿已进入输入框（弹层没吃掉输入）', (await page.evaluate(`return document.querySelector('textarea').value`)).includes('ok'), '')
  await page.press('Enter')
  await page.waitFor(`return /\\/conversations\\/[0-9a-zA-Z-]+/.test(location.pathname)`, { label: '会话创建并跳转', timeout: 40000 })
  const convId = await page.evaluate('return location.pathname.split("/").pop()')
  createdConversations.push(convId)
  await sleep(1500)
  await page.shot('07-conversation-created')
  console.log(`  → 会话 id = ${convId}`)

  // DoD：GET /conversations/:id 回显选定明细与钉住的指纹
  const detail = await api('GET', `/conversations/${convId}`)
  const d = detail.json.data ?? {}
  console.log(`  → GET /conversations/:id 关键字段: ${JSON.stringify({ capabilities: d.capabilities, capability_fingerprint: d.capability_fingerprint, agent_id: d.agent_id, runtime: d.runtime }).slice(0, 400)}`)
  check('会话详情回显能力选定明细', JSON.stringify(d.capabilities ?? null).includes(SKILLS.applicable), JSON.stringify(d.capabilities ?? null))
  const fp = d.capabilities?.fingerprint ?? d.capability_fingerprint
  check('会话详情回显钉住的能力指纹', typeof fp === 'string' && fp.length >= 8, `fingerprint=${String(fp).slice(0, 24)}`)
  check('会话落在所选 agent 上', d.agent_id === 'opencode', `agent_id=${d.agent_id} runtime=${d.runtime}`)

  // 界面侧：会话详情「能力」行
  const openDetail = await page.click(`[...document.querySelectorAll('button')].find(b => (b.getAttribute('aria-label')||b.getAttribute('title')||'') === '会话详情')`, { optional: true })
  if (openDetail) {
    await sleep(900)
    await page.shot('08-conversation-detail')
    const drawer = await page.text()
    const capLine = drawer.split('\n').find((l) => l.includes('技能')) ?? ''
    check('会话详情抽屉的「能力」行回显选定 skill', drawer.includes('能力') && drawer.includes(SKILLS.applicable), capLine.slice(0, 160))
    check('会话详情抽屉回显钉住的指纹前缀', typeof fp === 'string' && drawer.includes(fp.slice(0, 8)), `抽屉含 ${String(fp).slice(0, 8)}=${drawer.includes(String(fp).slice(0, 8))}`)
  } else {
    check('会话详情抽屉可打开（「详情」按钮）', false, '没找到「详情」按钮，选择器需按实际界面调整')
  }
  return { page, convId }
}

// ————————————————————————————————————————————————————————————————
// 场景二：M4-6 自动化页
// DoD（计划 §6）：建 daily → preview 对照 → 手动 run → 会话出现在列表且带标。
// ————————————————————————————————————————————————————————————————
async function scenarioAutomation(browser) {
  section('M4-6 自动化页（建 daily → preview → 手动运行 → 运行史 + origin 标）')
  const page = await openPage(browser, `${WEB_URL}/automations`)
  await page.waitFor(textExpr('新建任务'), { label: '自动化页渲染' })
  await page.shot('10-automations-empty')
  const empty = await page.text()
  check('自动化页可打开且有空态说明', /定时任务|自动化/.test(empty) && !/Application error/i.test(empty), empty.split('\n').slice(0, 4).join(' | ').slice(0, 160))

  await page.click(byText('button', '新建任务'))
  // 用页面文本判定，不用 antd 的类名（v6 改过类名：Tooltip 的容器就不是 .ant-tooltip 了）
  await page.waitFor(textExpr('ID（文件名，创建后不可改）'), { label: '编辑器打开' })
  await sleep(500)
  await page.shot('11-automation-editor')

  // 表单：id / 名称 / 调度三态 / agent / 工作空间 / prompt
  await page.type(q('input[placeholder="daily-deps-check"]'), AUTO_ID)
  await page.type(q('input[placeholder="每日依赖巡检"]'), '界面验收每日任务')

  if (process.env.DEBUG_FORM) {
    console.log('  [debug] 表单字段 id =', await page.evaluate(`return [...document.querySelectorAll('.ant-modal input')].map(i => i.id || i.className).join(' | ')`))
  }
  // 调度：三种 kind 各点一次，确认各自的附加字段按选择出现（once → 日期时间；weekly → 星期勾选）
  const tzDefault = await page.evaluate(`return (document.querySelector('.ant-select-content')||{}).innerText || ''`)
  check('时区有默认值（不需要用户先填）', /\//.test(tzDefault), `timezone=${tzDefault}`)
  const seg = (label) => `[...document.querySelectorAll('.ant-segmented-item')].find(b => (b.innerText||'').includes(${JSON.stringify(label)}))`
  await page.click(seg('一次'))
  await sleep(400)
  check('切「一次」出现时刻输入框', await page.evaluate(`return !!document.querySelector('input[type=datetime-local]')`), '')
  await page.click(seg('每周'))
  await sleep(400)
  check('切「每周」出现星期勾选组', (await page.count('input[type=checkbox]')) >= 7, `勾选框数=${await page.count('input[type=checkbox]')}`)
  await page.click(seg('每天'))
  await sleep(300)
  await page.shot('11b-automation-schedule')

  // Agent：默认已选一个（dsh），显式切到 opencode（与其它场景同一个底座，便于交叉对照）
  await page.pickSelect(`document.getElementById('agent_id').closest('.ant-select')`, 'opencode')
  const selText = (id) => page.evaluate(`return (document.getElementById(${JSON.stringify(id)}).closest('.ant-select').querySelector('.ant-select-content')||{}).innerText || ''`)
  const agentText = await selText('agent_id')
  check('Agent 已切到 opencode', agentText.includes('opencode'), `选中项=${agentText}`)

  // 工作空间：点药丸 → 取第一条候选目录（没有就手打绝对路径）→ 用药丸文案自证已选上
  const pillName = `(document.querySelector('.fg-ws-pill-name')||{}).innerText || ''`
  check('工作空间默认是「选择目录」', (await page.evaluate(`return ${pillName}`)) === '选择目录', await page.evaluate(`return ${pillName}`))
  await page.click(q('.fg-ws-pill'))
  await sleep(600)
  const manualInput = `input[placeholder^="输入绝对路径"]`
  const wsItems = await page.count('.fg-ws-item')
  check('工作空间选择器打开（有候选区与手输入口）', wsItems > 0 && (await page.box(q(manualInput))) !== null, `候选条目=${wsItems}`)
  // 点一条**带 title 的候选**（资料库/最近目录；apply 直接落值并收起弹层）。
  // 不带 title 的那条是「选择目录…」，它进浏览视图、不落值。
  // 手输路径走服务端校验，必须落在 MATE_WORKSPACE_ROOTS 内——`/tmp` 会被正当拒掉
  // （workspace_outside_roots），第一版就是拿 /tmp 撞了这条规则。
  const cand = `[...document.querySelectorAll('.fg-ws-item[title]')][0]`
  if (await page.box(cand)) {
    await page.click(cand)
  } else {
    const roots = await api('GET', `/workspaces/browse?path=${encodeURIComponent(process.env.HOME + '/.ark-mate/workspaces')}`)
    const p = roots.json.data?.path
    if (!p) throw new Error(`拿不到可用的工作区根：${JSON.stringify(roots.json).slice(0, 200)}`)
    await page.type(q(manualInput), p)
    await page.press('Enter')
  }
  await sleep(600)
  if ((await page.evaluate(`return ${pillName}`)) === '选择目录') {
    if (!(await page.box(q(manualInput)))) {
      await page.click(q('.fg-ws-pill'))
      await sleep(500)
    }
    const roots = await api('GET', `/workspaces/browse?path=${encodeURIComponent(process.env.HOME + '/.ark-mate/workspaces')}`)
    await page.type(q(manualInput), roots.json.data.path)
    await page.press('Enter')
    await sleep(600)
  }
  const cwdPicked = await page.evaluate(`return ${pillName}`)
  check('工作空间已选定（药丸显示目录名）', cwdPicked !== '选择目录' && cwdPicked.length > 0, `pill=${cwdPicked}`)
  await page.type(q('textarea[placeholder="每次触发时发给 agent 的话"]'), '只回复 ok。')
  await sleep(300)
  check('prompt 已填（保存按钮的前置）', (await page.evaluate(`return (document.querySelector('textarea[placeholder="每次触发时发给 agent 的话"]')||{}).value || ''`)).includes('ok'), '')
  await page.shot('12-automation-filled')

  // preview：保存之前就能看到"接下来 5 次"
  await clickJS(page, byText('button', '预览接下来 5 次'))
  await sleep(1500)
  await page.shot('13-automation-preview')
  const previewText = await page.text()
  const errLine = (previewText.match(/表单还没填完[^\n]*/) ?? previewText.match(/\[object Object\]/) ?? [''])[0]
  check('预览失败时给的是人话（不是 [object Object]）', !previewText.includes('[object Object]'), errLine)
  const hasPreview = /\d{4}-\d{2}-\d{2}/.test(previewText) || previewText.includes('没有未来的到期时刻')
  check('preview 在保存前给出未来到期时刻', hasPreview, previewText.split('\n').filter((l) => /\d{4}-\d{2}-\d{2}|没有未来/.test(l)).slice(0, 3).join(' | ').slice(0, 240))

  // 保存
  if (process.env.DEBUG_FORM) {
    console.log('  [debug] 弹窗内按钮 =', await page.evaluate(`return [...document.querySelectorAll('button')].map(b => JSON.stringify((b.innerText||'').replace(/\\s/g,'')) + '[' + b.className.slice(0,40) + ']').filter(t => t.length < 60).join(' | ').slice(0, 700)`))
  }
  // antd 会给两个汉字的按钮插一个空格（innerText 是"保 存"），所以比对前先去掉空白
  // 用 replaceAll(' ','') 而不是正则 /\\s/g：这段字符串本身是模板字面量，模板里的 \\s 会被
  // JS 降级成 's'（"保 存" 里的空格根本没被去掉），踩过一次。
  const saveBtn = `[...document.querySelectorAll('button')].filter(b => /^(保存|确定|创建)$/.test((b.innerText||'').trim().replaceAll(' ',''))).pop()`
  check('编辑器底部有保存按钮（okText=保存）', await page.evaluate(`return !!(${saveBtn})`), (await page.evaluate(`return (${saveBtn}||{}).innerText || '无'`)))
  await clickJS(page, saveBtn)
  await page.waitFor(`return !(document.body.innerText||'').includes('ID（文件名，创建后不可改）')`, { label: '编辑器关闭' })
  await sleep(900)
  await page.shot('14-automation-saved')
  const listed = await page.evaluate(`return document.body.innerText`)
  check('新建的自动化出现在列表', listed.includes(AUTO_ID), `含 ${AUTO_ID}=${listed.includes(AUTO_ID)}`)
  const apiList = await api('GET', '/automations')
  check('API 侧自动化已落盘', (apiList.json.data ?? []).some((a) => a.id === AUTO_ID), (apiList.json.data ?? []).map((a) => a.id).join(','))

  // 运行史（跑之前）：抽屉能开，空态如实
  await clickJS(page, byText('button', '运行史'))
  await page.waitFor(textExpr('的运行史'), { label: '运行史抽屉打开' })
  await sleep(1200)
  await page.shot('15-automation-runs-empty')
  check('运行史抽屉可开且空态如实', (await page.text()).includes('还没有触发记录'), '')

  // 手动 run（DoD：手动 run → 会话出现在列表且带标）
  // 注意手动运行**会跳到那条会话**（AutomationsPage.onRun 里 navigate），所以先看运行史再看运行。
  await page.goto(`${WEB_URL}/automations`, { waitText: AUTO_ID })
  const runBtn = byText('button', '立即运行')
  check('列表行有「立即运行」', !!(await page.evaluate(`return !!(${runBtn})`)), '')
  await clickJS(page, runBtn)
  await page.waitFor(`return location.pathname.startsWith('/conversations/')`, { label: '手动运行后跳到会话', timeout: 90000 })
  const autoConv = await page.evaluate('return location.pathname.split("/").pop()')
  createdConversations.push(autoConv)
  // 等这一轮**真正结束**再断言来源标：侧栏的 `origin` 标由"终态 → refreshConversations"带出
  // （列表是轮询刷新的，而事件流里没有 origin），固定 sleep 会在模型慢时假红——
  // 实测同一次运行：断言时无标，+5s 后 status=completed 且标出现。
  const st = await waitConversationTerminal(autoConv, 120000)
  check('自动化触发的会话跑到终态（等标的前提）', st !== 'timeout', `status=${st}`)
  await page.shot('16-automation-conversation')

  // 会话带标：侧栏/会话页上有 `title="由定时任务 <id> 触发"`
  const originTag = `[...document.querySelectorAll('[title]')].find(e => (e.getAttribute('title')||'').includes('由定时任务') && (e.getAttribute('title')||'').includes(${JSON.stringify(AUTO_ID)}))`
  let gotTag = false
  try {
    await page.waitFor(`return !!(${originTag})`, { label: '自动化来源标出现', timeout: 30000 })
    gotTag = true
  } catch {
    gotTag = false
  }
  check('会话带自动化来源标（由定时任务 … 触发）', gotTag, await page.evaluate(`return (${originTag}||{}).getAttribute ? (${originTag}).getAttribute('title') : '无'`))

  // API 交叉验证：这条会话的 origin 就是 automation:<id>
  const convDetail = await api('GET', `/conversations/${autoConv}`)
  check('会话 origin 落在 automation:ui-verify-daily', (convDetail.json.data?.origin ?? '') === `automation:${AUTO_ID}`, `origin=${convDetail.json.data?.origin}`)

  // 运行史（跑之后）：有记录、标明是手动触发
  await page.goto(`${WEB_URL}/automations`, { waitText: AUTO_ID })
  await clickJS(page, byText('button', '运行史'))
  await page.waitFor(textExpr('的运行史'), { label: '运行史抽屉打开（跑之后）' })
  await sleep(1500)
  await page.shot('17-automation-runs')
  const drawerText = await page.text()
  check('运行史出现记录且标明手动触发', /手动|manual/.test(drawerText), drawerText.split('\n').filter((l) => /手动|manual|完成|失败/.test(l)).slice(0, 3).join(' | ').slice(0, 220))
  const runsApi = await api('GET', `/automations/${AUTO_ID}/runs`)
  const recs = runsApi.json.data?.runs ?? runsApi.json.data?.records ?? []
  check('运行史 API 有记录', recs.length >= 1, JSON.stringify(recs[0] ?? null).slice(0, 240))
  check('运行史记录里有会话可回溯', recs.some((r) => r.conversation_id), (recs.map((r) => r.conversation_id).filter(Boolean)[0] ?? '无'))

  return { page }
}

// ————————————————————————————————————————————————————————————————
// 场景三：M5′ 命令发现面
// 依据：ADR-17 / README。opencode 会通告命令（含 `~/.agents/skills` 里的 skill），
//       面板输入框打 `/` 给候选；事件侧证据 = history 里有 session.commands。
// ————————————————————————————————————————————————————————————————
async function scenarioCommands(browser) {
  section('M5′ 命令发现面（opencode 通告 → 输入框 `/` 候选 + 事件侧证据）')
  // 挑一个**真的通告过命令**的 opencode 会话。
  // 注意：历史会话（M5′ 之前建的）永远不会有 `session.commands`——那时平台还没接收这条通告，
  // 所以"列表里第一条 opencode 会话"是错的判据（第一版就这么挑，选中 09-16 的老会话后必然红）。
  // 判据只能是"这条会话的 history 里真有该事件"；一条都没有就自己造一条。
  const oc = await ensureOpencodeConversation()
  if (!oc) {
    check('存在一个通告过命令的 opencode 会话', false, '既没找到也没能造出来')
    return {}
  }
  console.log(`  → 用会话 ${oc.id}（${oc.mode === 'created' ? '本轮新建' : '历史已通告'}）`)
  const page = await openPage(browser, `${WEB_URL}/conversations/${oc.id}`)
  await page.waitFor(`return !!document.querySelector('textarea')`, { label: '会话输入框就绪' })
  await sleep(2500) // 等 WS 全量回放把 session.commands 送进来
  await page.shot('20-conversation-opencode')

  // 事件侧证据（不依赖界面）：history 里应有 session.commands
  const hist = await api('GET', `/conversations/${oc.id}/history?limit=500`)
  const events = hist.json.data?.events ?? hist.json.data?.Events ?? []
  const cmdEv = events.find((e) => (e.type ?? e.Type) === 'session.commands')
  const cmds = cmdEv ? (cmdEv.payload?.commands ?? cmdEv.Payload?.commands ?? []) : []
  check('history 里有 session.commands 事件（底座确实通告）', !!cmdEv, cmdEv ? `commands=${cmds.length} 条：${cmds.slice(0, 5).map((c) => c.name).join(',')}` : `事件数=${events.length}`)
  if (cmds.length && cmds.some((c) => c.hint)) check('命令带 hint（入参提示）透传', true, cmds.filter((c) => c.hint).slice(0, 3).map((c) => `${c.name}:${c.hint}`).join(' | '))

  // 界面：输入 `/` → 候选弹层
  await page.type(q('textarea'), '/')
  await sleep(600)
  await page.shot('21-slash-hints')
  const open = await page.count('.fg-cmd-pop')
  const items = await page.evaluate(`return [...document.querySelectorAll('.fg-cmd-item')].map(b => b.innerText.trim().replace(/\\n/g,' ')).slice(0, 8)`)
  check('输入框打 `/` 弹出候选（.fg-cmd-pop）', open === 1, `弹层数=${open}`)
  check('候选来自底座通告（与事件列表一致）', items.length > 0 && (cmds.length === 0 || items.some((t) => cmds.some((c) => t.startsWith('/' + c.name)))), `候选=${items.join(' | ').slice(0, 240)}`)
  check('弹层写明「命令由底座提供，平台只做回显」', (await page.evaluate(`return document.querySelector('.fg-cmd-pop').innerText`)).includes('由底座提供'), '')
  // 候选多时页脚不能被滚走：它必须在滚动区（.fg-cmd-list）之外
  check('页脚说明常驻（不在 .fg-cmd-list 滚动区内）', await page.evaluate(`return !document.querySelector('.fg-cmd-list').contains(document.querySelector('.fg-cmd-foot'))`), '')

  // 键盘选中第一条 → 填入草稿（Enter 是"填入"而不是"发送"）
  if (items.length) {
    await page.press('ArrowDown')
    await page.press('Enter')
    const draft = await page.evaluate(`return document.querySelector('textarea').value`)
    check('Enter 把选中命令填入草稿（不是直接发送）', /^\/\S+ $/.test(draft), `草稿=${JSON.stringify(draft)}`)
    await page.shot('22-command-inserted')

    // 命令名里可以带 `/`（opencode 的 `git/branch` 就是这么通告的）：手打命名空间前缀时
    // 弹层不能消失。第一版按 `/` 截断，正是被这一步抓出来的。
    const ns = cmds.find((c) => c.name.includes('/'))
    if (ns) {
      const prefix = ns.name.split('/')[0]
      await setDraft(page, `/${prefix}/`)
      await sleep(500)
      await page.shot('23-namespaced-hints')
      const nsItems = await page.evaluate(`return [...document.querySelectorAll('.fg-cmd-item')].map(b => b.innerText.trim().split(/\s+/)[0])`)
      check(
        `手打命名空间前缀（/${prefix}/）时弹层仍在且候选为 ${prefix}/ 下的命令`,
        nsItems.length > 0 && nsItems.every((t) => t.startsWith(`/${prefix}/`)),
        `候选=${nsItems.slice(0, 6).join(' ')}`,
      )
    }
  }
  return { page }
}

/** 取一条 history 里真有 `session.commands` 的 opencode 会话；没有就现建一条并等通告到位。 */
async function ensureOpencodeConversation() {
  const convs = await api('GET', '/conversations?limit=30')
  for (const c of (convs.json.data ?? []).filter((c) => c.agent_id === 'opencode')) {
    const h = await api('GET', `/conversations/${c.id}/history?limit=200`)
    const ev = h.json.data?.events ?? h.json.data?.Events ?? []
    if (ev.some((e) => (e.type ?? e.Type) === 'session.commands')) return { ...c, mode: 'found' }
  }
  // 没有现成的：建 + 发一句话（ACP 会话在首轮 prompt 时真正起来，opencode 随即通告命令）
  const created = await api('POST', '/conversations', { agent_id: 'opencode' })
  const id = created.json.data?.id
  if (!id) return null
  createdConversations.push(id)
  await api('POST', `/conversations/${id}/prompt`, { text: '只回复 ok。' })
  for (let i = 0; i < 60; i++) {
    await sleep(500)
    const h = await api('GET', `/conversations/${id}/history?limit=200`)
    const ev = h.json.data?.events ?? h.json.data?.Events ?? []
    if (ev.some((e) => (e.type ?? e.Type) === 'session.commands')) return { id, mode: 'created' }
  }
  return { id, mode: 'created' } // 就算没等到也让后续断言如实报红
}

/** 直接改写 textarea 的 value 并派发 input（React 受控组件认这个）。 */
async function setDraft(page, text) {
  return page.evaluate(`
    const ta = document.querySelector('textarea');
    const setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value').set;
    setter.call(ta, ${JSON.stringify(text)});
    ta.dispatchEvent(new Event('input', { bubbles: true }));
    return ta.value;
  `)
}


// ————————————————————————————————————————————————————————————————
// v11「设置 · 模型」那一节（A11）：两个平级的管理对象 + 生效状态列 + 文案纪律。
//
// 覆盖的判据（计划 A11，逐条）：
//   ① 开设置 → 切到「模型」→ 两块标题是「供应商」「模型」，且**不出现 `provider` 做标题**；
//   ② 供应商：新建凭据 → 填密钥 → 自检（只跑本地段，零出网）→ 删除；
//   ③ 模型：切底座 → 改显示名 / 隐藏 / 置顶 → 点星号设默认 → 生效状态列有值 → 删除；
//   ④ **文案纪律**：保存成功的提示只能说「已保存，将在下一个会话进程生效」，不许说「已生效」。
//
// 全部用 REST 收尾（`finally`），跑完不留夹具。
//
// **本场景尚未在真 Chrome 上跑过**（写成时所在的环境起不了 GUI 浏览器，见文件头用法注释）：
// 首次 `pnpm ui:accept model` 很可能需要按真实 DOM 微调选择器——这正是它默认不进默认集合的原因。
async function scenarioSettingsModel(browser) {
  section('v12 设置 · 模型（供应商 + 模型两块 / 单一默认 / 聚合生效结论 / 零底座词汇）')
  const provID = 'ui-verify-prov'
  const modelID = `ui-verify-${Date.now().toString(36)}`
  const page = await openPage(browser, `${WEB_URL}/`)
  const cleanup = async () => {
    // 逐条 best-effort：夹具是自己造的，跑完必须自己清（顺序：模型 → 供应商，因为模型挂靠供应商）。
    // 按**前缀**扫而不是只删本次那条：中途失败的那几次也会留下同一前缀的夹具。
    // v12 起删默认模型**不再被拒**（B4：连带清空默认），所以不必先清默认。
    for (const m of ((await api('GET', '/models')).json.data?.models ?? [])) {
      if (!String(m.model_id).startsWith('ui-verify-')) continue
      await api('DELETE', `/models/${m.id}`)
    }
    await api('DELETE', `/providers/${provID}`)
  }
  try {
    // ① 左栏「设置」→ 弹窗内切到「模型」
    await page.waitFor(textExpr('设置'), { label: '左栏渲染' })
    await page.click(btnExpr('设置'))
    await page.waitFor(`const el = document.querySelector('.fg-setdlg'); return !!el && el.getClientRects().length > 0`, { label: '设置弹窗打开' })
    await page.click(`[...document.querySelectorAll('.fg-setdlg-item')].find(b => (b.innerText||'').includes('模型'))`)
    await page.waitFor(`return !!document.querySelector('.fg-model-panel')`, { label: '模型那一节渲染' })
    await sleep(600)
    await page.shot('20-settings-model')

    const blocks = await page.evaluate(
      `return [...document.querySelectorAll('.fg-model-panel section[aria-label]')].map(s => s.getAttribute('aria-label'))`,
    )
    check('两块标题恰好是「供应商」与「模型」', JSON.stringify(blocks) === JSON.stringify(['供应商', '模型']), JSON.stringify(blocks))
    const titles = await page.evaluate(
      `return [...document.querySelectorAll('.fg-model-panel header b')].map(b => (b.innerText||'').trim())`,
    )
    check('表头标题不含代码词', titles.length >= 2 && !titles.some((t) => /provider|mcp|skill|runtime|dsh|opencode/i.test(t)), JSON.stringify(titles))

    // ② 供应商：新建 → 自检（本地段）→ 留在列表里
    await page.click(btnExpr('新建供应商'))
    await page.waitFor(`return !!document.querySelector('.ant-modal input#id')`, { label: '供应商表单打开' })
    await page.type('document.querySelector(".ant-modal input#id")', provID)
    check('供应商表单的 id 已填进去',
      (await page.evaluate(`return document.querySelector('.ant-modal input#id')?.value`)) === provID,
      String(await page.evaluate(`return document.querySelector('.ant-modal input#id')?.value`)))
    // 协议是 antd Select（不是裸 input）：**断言它开箱就是 openai**，不去手动选。
    //
    // 为什么改（2026-10-10）：手动走一遍 `selectByFieldID`（点开 → 输入 → 点选项）会在
    // 这里把表单值弄丢（实测：走完那三步后 `validateFields` 仍报「协议必填」），
    // 而"默认就是 openai"本来就是产品该有的形态——`Form` 的 `initialValues` 提供它。
    // 断言默认值比"能手动选"更强：它直接钉住那条初值链路。
    const protoShown = await page.evaluate(
      `const i = document.querySelector('.ant-modal input#protocol'); return i ? (i.closest('.ant-select')?.innerText || '') : ''`,
    )
    check('供应商表单的「协议」开箱即 openai（表单初值链路生效）',
      String(protoShown).includes('openai'), JSON.stringify(protoShown))
    // 关掉 Select 的下拉浮层：它盖在表单上，会让随后的 `page.click(input)` 落到浮层上
    // （真实鼠标事件被遮住 = 输入框根本没聚焦，insertText 打进空气里）。
    //
    // **不要用 Escape**：antd 的 Modal 默认 `keyboard`，那个按键会连带把**整个弹窗**关掉
    // （实测：之后 form 填得好好的、点保存什么也不发生）。改成点一下弹窗标题这个中性区域。
    await page.click(`document.querySelector('.ant-modal .ant-modal-title')`, { optional: true })
    await sleep(250)
    await page.type('document.querySelector(".ant-modal input#base_url")', 'https://api.deepseek.com/v1')
    await page.type('document.querySelector(".ant-modal input#api_key")', 'sk-ui-verify')
    const filled = await page.evaluate(`return JSON.stringify({
      id: document.querySelector('.ant-modal input#id')?.value,
      base_url: document.querySelector('.ant-modal input#base_url')?.value,
      api_key: (document.querySelector('.ant-modal input#api_key')?.value || '').length,
    })`)
    check('供应商表单的端点与密钥已填进去', /"base_url":"https:\/\/api\.deepseek\.com\/v1"/.test(filled) && /"api_key":[1-9]/.test(filled), filled)
    // 保存：按**完整鼠标序列**派发（mousedown → mouseup → click）。
    //
    // 为什么不用 `el.click()`：antd 的 Button 在受控弹窗里对合成 `click` 不总是响应（实测：
    // 事件派发了、React 的 onClick 没跑）。完整的鼠标序列与真实点击同形，React 一定收得到；
    // 而 `page.click`（CDP 真实鼠标）在**嵌套弹窗**里坐标会被外层元素吃掉。
    // 保存：用**真实 CDP 鼠标事件 + 由 getBoundingClientRect 算出的坐标**。
    //
    // 三条失败过的路都记在这里，免得下一个人重走：
    //   ① `el.click()`（合成点击）→ React 的 onClick 不跑（事件没经过真正的输入管线）；
    //   ② `document.dispatchEvent(new MouseEvent(...))` → 同上；
    //   ③ `page.click`（CDP 鼠标 + DOM.getBoxModel 的坐标）→ 在**嵌套弹窗**里盒子算出来偏了，
    //      事件落到遮罩上（表现：点了没反应，也没有任何错误）。
    // 现在这条把"真实输入管线"和"准确的矩形"两半各自取对。
    // 保存：一次**真实鼠标点击**（坐标取自按钮的 `getBoundingClientRect`）。
    //
    // 记一条踩过的坑，免得下一个人重走：这一步早期在**手搭的隔离核**上跑不通，表现是
    // "点了没反应、库里也没有"，一度被误判成"headless 送不进鼠标事件"。真因是环境——
    // `make dev` 会给核设 `MATE_ALLOWED_ORIGINS`（vite 源），而隔离核没有：浏览器的**同源
    // GET 不带 `Origin`**（所以列表读得到），而 **POST 一定带** → 核的 Origin 矩阵回 403。
    // 症状与"输入没送到"一模一样。跑这条验收时，核必须带 `MATE_ALLOWED_ORIGINS`。
    const saveRect = await page.evaluate(`return (() => {
      const el = ${saveBtnExpr('base_url')}
      if (!el) return null
      if (el.disabled || el.classList.contains('ant-btn-loading')) return { disabled: el.className }
      const r = el.getBoundingClientRect()
      return { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) }
    })()`)
    if (!saveRect) throw new Error('找不到供应商表单的「保存」按钮：' + saveBtnExpr('base_url'))
    if (saveRect.disabled) throw new Error('供应商表单的「保存」被禁用：' + saveRect.disabled)
    for (const type of ['mouseMoved', 'mousePressed', 'mouseReleased']) {
      await send2(page, { type, x: saveRect.x, y: saveRect.y, button: 'left', clickCount: 1 })
    }
    const created = await page
      .waitFor(`return (document.body.innerText||'').includes(${JSON.stringify(provID)})`, { label: '供应商出现在列表里', timeout: 10000 })
      .catch(() => false)
    if (!created) {
      const toasts = await page.evaluate(`return [...document.querySelectorAll('.ant-message-notice-content')].map(e => e.innerText).join(' | ')`)
      const ids = (await api('GET', '/providers')).json.data ?? []
      const errs = await page.evaluate(
        `return [...document.querySelectorAll('.ant-modal .ant-form-item-explain-error')].map(e => e.innerText).join(' | ')`,
      )
      const btns = await page.evaluate(
        `return [...document.querySelectorAll('.ant-modal button')].map(b => (b.innerText||'').trim()).filter(Boolean).join(',')`,
      )
      throw new Error('供应商没出现在列表里：toast=' + toasts + ' 表单=' + filled +
        ' 校验错误=' + errs + ' 按钮=' + btns + ' 库里=' + JSON.stringify(ids.map((p) => p.id)) +
        ' 页面错误=' + (page.errors || []).join(' || '))
    }
    check('供应商创建后出现在供应商表里', true, provID)
    // 端点列有值（缺端点会显示「缺端点」Tag——那条路径由 providers 的 problems 单测覆盖）
    const provRow = await page.evaluate(`return document.querySelector('.fg-model-panel').innerText`)
    check('供应商表给出端点与密钥状态', /api\.deepseek\.com/.test(provRow) && /已存|未设置/.test(provRow),
      provRow.split('\n').filter((l) => /deepseek|密钥|设置/.test(l)).join(' | ').slice(0, 200))
    await page.shot('21-settings-provider-created')

    // 自检：只跑本地段（**绝不点那条会出网的**）
    const probeBtnExpr = `[...document.querySelectorAll('.fg-model-panel button')].find(b => (b.innerText||'').replace(/\\s+/g,'').includes('自检'))`
    const clicked = await clickJS(page, probeBtnExpr)
    const opened = await page.waitFor(`return !!document.querySelector('.fg-probe')`, { label: '自检结果展开', timeout: 40000 }).catch(() => false)
    if (!opened) {
      const toasts = await page.evaluate(`return [...document.querySelectorAll('.ant-message-notice-content')].map(e => e.innerText).join(' | ')`)
      throw new Error(`自检结果没展开：clicked=${clicked} toast=${toasts}`)
    }
    await sleep(500)
    const probe = await page.evaluate(`return document.querySelector('.fg-probe').innerText`)
    // v12 的本地段检查项：exists / protocol / base_url / secret / refs / delivery / catalog
    check('自检逐条给出结论（含端点与密钥两项）',
      /exists/.test(probe) && /base_url/.test(probe) && /secret/.test(probe),
      probe.split('\n').slice(0, 4).join(' | ').slice(0, 240))
    check('自检有「连一次上游」的显式入口（默认不出网）', /连一次上游/.test(probe), probe.slice(0, 160))

    // ③ 模型：新建 → 出现在清单里（v12 的表单字段是 vendor / model_id）
    const openedModel = await clickJS(page, btnExpr('新建模型'))
    const modelModalOpen = await page
      .waitFor(`return !!document.querySelector('.ant-modal input#model_id')`, { label: '模型表单打开', timeout: 8000 })
      .catch(() => false)
    if (!modelModalOpen) {
      throw new Error('新建模型没打开表单：clickJS=' + openedModel)
    }
    const provPick = await selectByFieldID(page, 'vendor', provID)
    check('模型表单的「供应商」已选中该供应商', provPick.ok, provPick.detail)
    await page.type('document.querySelector(".ant-modal input#model_id")', modelID)
    check('模型表单的 model_id 已填进去',
      (await page.evaluate(`return document.querySelector('.ant-modal input#model_id')?.value`)) === modelID,
      await page.evaluate(`return JSON.stringify({
        v: document.querySelector('.ant-modal input#model_id')?.value,
        html: document.querySelector('.ant-modal input#model_id')?.outerHTML.slice(0, 160),
        ids: [...document.querySelectorAll('.ant-modal input')].map(i => i.id),
      })`))
    await page.click(saveBtnExpr('model_id'))
    await sleep(350)
    const toast = await page.evaluate(
      `return [...document.querySelectorAll('.ant-message-notice-content')].map(e => e.innerText).join(' | ')`,
    )
    const inList = await page
      .waitFor(`return (document.body.innerText||'').includes(${JSON.stringify(modelID)})`, { label: '模型出现在清单里', timeout: 10000 })
      .catch(() => false)
    if (!inList) {
      const list = (await api('GET', '/models')).json.data?.models ?? []
      throw new Error('模型没出现在清单里：toast=' + toast +
        ' 表单里的 model_id=' + await page.evaluate(`return document.querySelector('.ant-modal input#model_id')?.value`) +
        ' 库里=' + JSON.stringify(list.map((m) => m.model_id)))
    }
    await sleep(400)
    await page.shot('22-settings-model-created')

    // ④ 文案纪律（ADR-22）：面板里**不许**出现任何底座词汇，也不许渲染 sync[] 的机器面字段。
    const panelCopy = await page.evaluate(`return document.querySelector('.fg-model-panel').innerText`)
    const lower = panelCopy.toLowerCase()
    const banned = ['dsh', 'opencode', 'dsh', 'runtime', '底座', 'provider']
    const hit = banned.filter((w) => lower.includes(w))
    check('面板正文零底座词汇（ADR-22）', hit.length === 0,
      hit.length ? `命中 ${JSON.stringify(hit)}；行=${JSON.stringify(panelCopy.split('\n').filter((l) => hit.some((w) => l.toLowerCase().includes(w))))}` : '干净')
    check('逐后端的机器面明细没有被渲染（sync[] 属诊断面）',
      !/dsh|opencode/.test(panelCopy), panelCopy.slice(0, 160))

    // 生效结论：三态之一 + 生效说明说清「保存成功 ≠ 已生效」。
    check('生效结论是三态之一（已生效/待生效/同步失败）',
      /同步状态：(已生效|待生效|同步失败)/.test(panelCopy),
      panelCopy.split('\n').filter((l) => /同步状态/.test(l)).join(' | ').slice(0, 200))
    check('生效说明说清「保存成功只表示写入平台清单、要等新会话进程」',
      /保存成功/.test(panelCopy) && /新/.test(panelCopy) && /会话进程|进程启动/.test(panelCopy),
      panelCopy.split('\n').filter((l) => /保存成功|进程/.test(l)).join(' | ').slice(0, 240))
    check('保存提示（捕获到时）不承诺"已生效"',
      toast === '' || !/已生效/.test(toast), JSON.stringify(toast).slice(0, 200))

    // A14：默认模型选择器**恰好一个**（结构判据，前端半场的界面版本）。
    const defaultPickers = await page.count('.fg-model-panel [aria-label="默认模型"]')
    check('默认模型选择器恰好一个（A14）', defaultPickers === 1, `count=${defaultPickers}`)

    // ⑤ 设默认 → 界面跟随 → 删掉它**允许**且连带清空（B4）
    const list = (await api('GET', '/models')).json.data?.models ?? []
    const row = list.find((m) => m.model_id === modelID)
    if (!row) throw new Error('刚建的模型不在清单里：' + JSON.stringify(list.map((m) => m.model_id)))
    const patched = await api('PATCH', `/models/${row.id}`, { is_default: true })
    check('把该条设为默认（REST）', patched.json.code === 0, JSON.stringify(patched.json).slice(0, 160))
    // 界面上刷新一次（**模型块**的刷新——`btnExpr('刷新')` 会先命中供应商块那个），
    // 断言选择器真的显示了它（默认是**单一值**，不是逐后端映射）。
    await clickJS(page,
      `[...document.querySelectorAll('section[aria-label="模型"] button')].find(b => (b.innerText||'').replace(/\\s+/g,'') === '刷新')`)
    await sleep(800)
    const shown = await page.evaluate(
      `const el = document.querySelector('.fg-model-panel [aria-label="默认模型"]'); return el ? (el.closest('.ant-select')?.innerText || el.value || '') : ''`,
    )
    check('界面上的默认模型选择器显示该条', String(shown).includes(modelID), String(shown).slice(0, 120))
    await page.shot('23-settings-model-default')

    const del = await api('DELETE', `/models/${row.id}`)
    check('删掉默认模型**允许**且如实回带 default_cleared（B4）',
      del.json.code === 0 && del.json.data?.default_cleared === true, JSON.stringify(del.json).slice(0, 200))
  } finally {
    await cleanup()
  }
  return { page }
}

// ————————————————————————————————————————————————————————————————
async function main() {
  const only = process.argv.slice(2).filter((a) => !a.startsWith('-'))
  const health = await api('GET', '/health')
  check('后端 health 可达', health.json.code === 0, `${MATE_URL} → ${JSON.stringify(health.json)}`)
  const webRoot = await fetch(WEB_URL).then((r) => r.status)
  check('前端入口可达（vite）', webRoot === 200, `${WEB_URL} → HTTP ${webRoot}`)

  await seedSkills()
  check('验收用 skill 已就绪（M2 写面）', true, Object.values(SKILLS).join(', '))

  const { child, port } = await launchChrome()
  const browser = await connectCDP(
    (await (await fetch(`http://127.0.0.1:${port}/json/version`)).json()).webSocketDebuggerUrl,
  )

  // `model` 是**可选场景**（默认不跑）：见文件头的用法注释——它还没在真 Chrome 上跑过一次。
  const scenarios = { cap: scenarioCapability, auto: scenarioAutomation, cmd: scenarioCommands, model: scenarioSettingsModel }
  const names = only.length ? only : ['cap', 'cmd', 'auto', 'model']
  for (const n of names) {
    if (!scenarios[n]) throw new Error(`未知场景: ${n}（可选 ${Object.keys(scenarios).join('/')}）`)
    let out = {}
    try {
      out = (await scenarios[n](browser)) ?? {}
    } catch (e) {
      check(`${n}: 场景执行完成`, false, String(e.message ?? e))
      const fresh = globalPageErrors.filter((x) => !/favicon|DevTools|ResizeObserver/i.test(x)).slice(-6)
      if (fresh.length) console.log(`  ⚠️  ${n}: 失败时的控制台错误 — ${fresh.join(' || ').slice(0, 600)}`)
    }
    if (out.page) {
      const noise = (e) => /favicon|DevTools|ResizeObserver/i.test(e)
      const all = out.page.errors.filter((e) => !noise(e))
      const warns = all.filter((e) => /^Warning:/.test(e))
      const errs = all.filter((e) => !/^Warning:/.test(e))
      check(`${n}: 页面无未捕获异常/无错误级控制台输出`, errs.length === 0, errs.slice(0, 3).join(' || ').slice(0, 300))
      if (warns.length) console.log(`  ⚠️  ${n}: 控制台有 ${warns.length} 条 React 警告（非异常，记录备查）：${[...new Set(warns)].slice(0, 4).join(' || ').slice(0, 400)}`)
    }
  }

  // 收尾：默认 agent 切回 dsh（环境还原），并清理验收用 skill
  await setDefaultAgent('dsh')
  const after = await api('GET', '/agents')
  check('默认 agent 已还原为 dsh', (after.json.data ?? []).find((a) => a.id === 'dsh')?.is_default === true, '')
  await cleanupSkills()
  await cleanupCreated()

  if (!process.env.KEEP_CHROME) child.kill('SIGKILL')
  const failed = results.filter((r) => !r.ok)
  console.log(`\n——— 汇总：${results.length - failed.length}/${results.length} 通过 ———`)
  if (failed.length) {
    for (const f of failed) console.log(`  ❌ ${f.name} — ${f.detail}`)
    process.exitCode = 1
  }
}

main().catch((e) => {
  console.error('验收脚本自身失败:', e)
  process.exitCode = 2
})

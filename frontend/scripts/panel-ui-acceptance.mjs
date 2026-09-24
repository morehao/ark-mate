#!/usr/bin/env node
/**
 * 面板界面验收（T-4：M3-4 能力勾选面板 / M4-6 自动化页 / M5′ 命令补全）——**真 Chrome + 真服务**。
 *
 * 为什么这里才出现浏览器驱动代码：本仓前端零测试依赖（无 vitest/jest/playwright/puppeteer），
 * 既有 `smoke-e2e.mjs` / `e2e-live-turn.mjs` 只走 REST/WS。而 M3-4 与 M4-6 的 DoD 是**手工界面
 * 验收**（勾选后建会话、preview 对照、手动 run、会话带标跳转），REST 冒烟替不了"界面真的长出来了"。
 * 于是用 Node 24 自带的全局 WebSocket 直连 CDP——**不引入任何新依赖**。
 *
 * 用法：
 *   node scripts/panel-ui-acceptance.mjs              # 全部场景
 *   node scripts/panel-ui-acceptance.mjs cap auto cmd # 只跑指定场景
 *   KEEP_CHROME=1 ...                                 # 保留 Chrome 便于人工接管
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

const PANEL_URL = process.env.PANEL_URL ?? 'http://localhost:5173'
const FORGE_URL = process.env.FORGE_URL ?? 'http://127.0.0.1:10001'
const CHROME = process.env.CHROME_BIN ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const SHOT_DIR = process.env.SHOT_DIR ?? join(REPO_ROOT, '.dsh/docs/notes/2026-09-24-panel-ui-acceptance')
const PROFILE = join(tmpdir(), `forge-ui-accept-${process.pid}`)
const SKILLS = { applicable: 'ui-verify-oc', inapplicable: 'ui-verify-dsh' }
const AUTO_ID = 'ui-verify-daily'

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

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
    }
    if (msg.method === 'Log.entryAdded' && msg.params.entry.level === 'error') {
      errors.push(`${msg.params.entry.source}: ${msg.params.entry.text}`)
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
async function api(method, path, body) {
  const res = await fetch(`${FORGE_URL}${path.startsWith('/v1') ? path : `/v1/forge${path}`}`, {
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
    [SKILLS.inapplicable, ['deepseekharness'], 'UI验收-仅dsh'],
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
  const page = await openPage(browser, `${PANEL_URL}/agents`)
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
  await page.goto(`${PANEL_URL}/`, { waitText: '可用的能力' })
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
    check('hover 不适用项给出原因（tooltip）', /runtimes|不相交|deepseekharness/.test(tip), tip.slice(0, 200))
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
  const page = await openPage(browser, `${PANEL_URL}/automations`)
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
  // 手输路径走服务端校验，必须落在 FORGE_WORKSPACE_ROOTS 内——`/tmp` 会被正当拒掉
  // （workspace_outside_roots），第一版就是拿 /tmp 撞了这条规则。
  const cand = `[...document.querySelectorAll('.fg-ws-item[title]')][0]`
  if (await page.box(cand)) {
    await page.click(cand)
  } else {
    const roots = await api('GET', `/workspaces/browse?path=${encodeURIComponent(process.env.HOME + '/.forge/workspaces')}`)
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
    const roots = await api('GET', `/workspaces/browse?path=${encodeURIComponent(process.env.HOME + '/.forge/workspaces')}`)
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
  await page.goto(`${PANEL_URL}/automations`, { waitText: AUTO_ID })
  const runBtn = byText('button', '立即运行')
  check('列表行有「立即运行」', !!(await page.evaluate(`return !!(${runBtn})`)), '')
  await clickJS(page, runBtn)
  await page.waitFor(`return location.pathname.startsWith('/conversations/')`, { label: '手动运行后跳到会话', timeout: 40000 })
  const autoConv = await page.evaluate('return location.pathname.split("/").pop()')
  createdConversations.push(autoConv)
  await sleep(2500)
  await page.shot('16-automation-conversation')

  // 会话带标：侧栏/会话页上有 `title="由定时任务 <id> 触发"`
  const originTag = `[...document.querySelectorAll('[title]')].find(e => (e.getAttribute('title')||'').includes('由定时任务') && (e.getAttribute('title')||'').includes(${JSON.stringify(AUTO_ID)}))`
  check('会话带自动化来源标（由定时任务 … 触发）', !!(await page.evaluate(`return !!(${originTag})`)), await page.evaluate(`return (${originTag}||{}).getAttribute ? (${originTag}).getAttribute('title') : '无'`))

  // API 交叉验证：这条会话的 origin 就是 automation:<id>
  const convDetail = await api('GET', `/conversations/${autoConv}`)
  check('会话 origin 落在 automation:ui-verify-daily', (convDetail.json.data?.origin ?? '') === `automation:${AUTO_ID}`, `origin=${convDetail.json.data?.origin}`)

  // 运行史（跑之后）：有记录、标明是手动触发
  await page.goto(`${PANEL_URL}/automations`, { waitText: AUTO_ID })
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
  const page = await openPage(browser, `${PANEL_URL}/conversations/${oc.id}`)
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
async function main() {
  const only = process.argv.slice(2).filter((a) => !a.startsWith('-'))
  const health = await api('GET', '/health')
  check('后端 health 可达', health.json.code === 0, `${FORGE_URL} → ${JSON.stringify(health.json)}`)
  const panel = await fetch(PANEL_URL).then((r) => r.status)
  check('面板可达（vite）', panel === 200, `${PANEL_URL} → HTTP ${panel}`)

  await seedSkills()
  check('验收用 skill 已就绪（M2 写面）', true, Object.values(SKILLS).join(', '))

  const { child, port } = await launchChrome()
  const browser = await connectCDP(
    (await (await fetch(`http://127.0.0.1:${port}/json/version`)).json()).webSocketDebuggerUrl,
  )

  const scenarios = { cap: scenarioCapability, auto: scenarioAutomation, cmd: scenarioCommands }
  const names = only.length ? only : ['cap', 'cmd', 'auto']
  for (const n of names) {
    if (!scenarios[n]) throw new Error(`未知场景: ${n}（可选 ${Object.keys(scenarios).join('/')}）`)
    let out = {}
    try {
      out = (await scenarios[n](browser)) ?? {}
    } catch (e) {
      check(`${n}: 场景执行完成`, false, String(e.message ?? e))
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

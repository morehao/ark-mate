#!/usr/bin/env node
/**
 * Web 界面验收（T-4：M3-4 能力勾选面板 / M4-6 自动化页 / M5′ 命令补全 / v11「设置 · 模型」一节）——**真 Chrome + 真服务**。
 *
 * 为什么这里才出现浏览器驱动代码：本仓前端零测试依赖（无 vitest/jest/playwright/puppeteer），
 * 既有 `smoke-e2e.mjs` / `e2e-live-run.mjs` 只走 REST/WS。而 M3-4 与 M4-6 的 DoD 是**手工界面
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
 * 前置：`make dev` 已起（后端 :10001，**它服务的是一份构建产物**）。脚本**不启动服务**——
 * 它验的是"人在看的那个界面"，偷偷另起一个服务就验错了对象。
 *
 * **靶就是产物（2026-10-11，P2）**：默认靶 `http://127.0.0.1:10001`（= `make dev` 的浏览器入口，它
 * 服务 `frontend/apps/web/dist`）。开跑前有一条**启动断言**逐字核对"这个靶给的 index.html 就是本地
 * dist 那份、它的哈希资产真的取得到"——指到 vite dev server（从源码现编译）上会**当场红**，因为那
 * 正是 2026-10-11 之前的老形态：**验过的界面从未在交付件形态上跑过**。判据与守卫见
 * `backend/internal/archtest/dev_parity_test.go` 与 `.dsh/docs/plans/2026-10-11-dev-delivery-parity-plan.md`。
 *
 * **场景按在册 runtime 驱动（同批改造）**：本构建固定了底座（用户不能选 runtime），所以"哪个 agent
 * 在册、哪条 skill 适用"是**服务端事实**——脚本从 `GET /agents` 读，不写死任何具体底座名（历史实测
 * 2026-10-11 的 P0：写死会把"脚本陈旧"演成"等待超时：页面渲染不出来"，误导了整整两轮）；验不了的
 * 场景**显式跳过并写明理由**，不靠上一轮残留数据"碰巧绿"。
 *
 * 环境写入（脚本自己造、自己清）：用 M2 写面给**每个在册 agent** 各建一条验收用 skill（钉它自己的
 * runtime），另加一条钉"不在册 runtime"的不可达项；若在册 agent 多于一个则切一次当前 agent
 * （M3-4 的清单按 agent 视角取）并在结束时**切回原来的那个**。
 */

import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { basename } from 'node:path'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

// 仓库根按脚本位置定位：`pnpm ui:accept` 的 cwd 是 frontend/，用相对路径会把截图写进 frontend/.dsh/
const REPO_ROOT = fileURLToPath(new URL('../..', import.meta.url))

const WEB_URL = process.env.WEB_URL ?? 'http://127.0.0.1:10001'
const MATE_URL = process.env.MATE_URL ?? 'http://127.0.0.1:10001'
const CHROME = process.env.CHROME_BIN ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const SHOT_DIR = process.env.SHOT_DIR ?? join(REPO_ROOT, '.dsh/docs/notes/2026-09-24-panel-ui-acceptance')
const PROFILE = join(tmpdir(), `mate-ui-accept-${process.pid}`)
/** 产物目录：启动断言拿它核对"靶服务的确实是这一份"（与 `make dev` 的 `--web-dir` 同源）。 */
const WEB_DIR = join(REPO_ROOT, 'frontend/apps/web/dist')
/**
 * 验收用 skill 定义：**每个在册 agent 一条**（钉它自己的 runtime），外加一条钉"不在册 runtime"的
 * 不可达项——脚本自建自清。
 *
 * 这不是一张写死的 id 表：定义由 `orientSkills()` 按 `GET /agents` 的**在册事实**现算
 * （多 agent 时切换当前 agent，适用/不适用两个角色跟着换）。
 */
let SKILL_DEFS = {}
/**
 * 单 agent 构建下"不适用"那条 skill 钉的占位 runtime 标识：它刻意**不在任何在册 agent 的候选
 * runtime 里**，所以清单里必须有它、且必须标"不可选"。这条判据验的是"不适用也列出来、不隐藏"，
 * 不需要真的存在第二个底座（本构建只装配一个 adapter，用户也不能选 runtime）。
 */
const ABSENT_RUNTIME = 'not-registered'
/** 当前在册的 runtime（= 当前 agent 的 id）与"另一个"：多 agent 时是另一条在册 agent，否则是占位标识。 */
let RUNTIME = ''
let OTHER_RUNTIME = ''
/** `{ applicable, inapplicable }` 的 skill id（按 RUNTIME 定向）。 */
let SKILLS = {}
/** `GET /agents` 的最近一次事实（场景可能切换当前 agent，切完要重取）。 */
let AGENTS = []
/** 开跑时的当前 agent：收尾要切回**它**（按 `GET /agents` 的事实取，不是写死某个底座名）。 */
let INITIAL_DEFAULT_AGENT = null
const AUTO_ID = 'ui-verify-daily'
/**
 * 目录浏览对话框场景的夹具目录名（在 `mkdtempSync(tmpdir()/mate-wsdir-)` 下自建自清）。
 *
 * 为什么夹具必须长在**临时目录**里：这个场景会经服务端真建一个目录（`POST /workspaces/dirs`），
 * 而它是整个验收里唯一往磁盘写新文件的动作。临时目录保证"最坏情况也只弄脏自己造的那棵树"
 * （本机隔离纪律：开发期脚本绝不碰用户自己的目录）。
 */
const WSDIR = { keep: 'ui-verify-keep', created: 'ui-verify-created', hidden: '.ui-verify-hidden', file: 'notes.md' }

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// 全局控制台错误：场景抛错时一并打印。**只加不减**——真出问题时"只有一句超时"是最难查的形态，
// 而控制台里的真相（403、未捕获异常、antd 警告）就在手边。
const globalPageErrors = []

// 脚本自己造出来的会话（收尾统一归档：不留垃圾在本地开发实例里）
const createdSessions = []

// ——— 断言台账 ———
const results = []
/**
 * 显式跳过：**不计入红**，但必须在汇总里单独列出来（含理由）。
 *
 * 为什么要有它：有些场景的前提是"某个底座在册"（例如 `/` 命令补全需要会通告命令的 runtime），
 * 而底座现在由**构建期固定**——那种场景在本构建上根本验不了。此时只有两条诚实的出路：显式跳过
 * 并写明理由，或者让它红。**绝不接受第三种**：靠上一次跑剩下的数据"碰巧绿"（历史实测 2026-10-11
 * `cmd` 正是这种假绿——它找到的是一条早先由本脚本创建的、属于某个**当时在册、如今已不在仓库**的
 * adapter 的会话，于是"验过了"其实什么也没验）。
 */
const skips = []
function skip(name, why) {
  skips.push({ name, why })
  console.log(`  ⏭️  ${name} — 跳过：${why}`)
}
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

/**
 * 等编辑卡**收起**（= 这张卡的写面已返回、表单已卸载）。
 *
 * 为什么必须有它（2026-10-13 实测，连着两次假红）：保存期间面板 `busy`，而开新卡的那些按钮
 * 带着 `disabled={busy}`——上一张卡还没收起时点「添加模型」，这一下**被 disabled 吃掉**、
 * 什么也不发生（不报错）；而随后那条"等新卡展开"会被**上一张还没收起的卡**满足（两者都是
 * `.fg-editor` 里的 `input#model_id`）。于是脚本以为新卡开了、其实在往旧卡里填，等旧卡的写面
 * 返回把它收起，现场就变成"元素凭空消失"（dump 实测：`editors: []`、行卡还在、面板还在）。
 * 固定 `sleep(800)` 赌不赢这件事——写面可能同步拉起底座（实测 `/models/catalog` 单次 3.1s）。
 */
const waitEditorGone = (page, label = '编辑卡收起') =>
  page.waitFor('return !document.querySelector(".fg-editor")', { label, timeout: 20000 })

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
 * 取**包含某个字段**的那张就地展开的编辑卡里的提交按钮（按文本、忽略空白）。
 *
 * 为什么不能直接 `[...document.querySelectorAll('button')].find(...)`：设置 · 模型的编辑
 * 自 2026-10-11 起不再是弹窗，而是卡片里就地展开的一张 `.fg-editor`——而**同一时刻可能同时开着
 * 两张**（供应商块一张、模型块一张）。"第一个文本是「保存」的按钮"会命中另一块里那个，
 * 症状与当年一样：点「保存」什么都没发生，或者把另一张卡提交了一遍。
 * 按字段所在的那张卡限定作用域即可。
 *
 * 文案随卡片角色不同（**新建**是「创建供应商 / 创建模型」，**编辑**是「保存」）——
 * 判据是"这张卡里那个提交按钮"，不是某个固定字样，所以两种都收。
 */
function saveBtnExpr(fieldID) {
  return `(() => {
    const inp = document.querySelector('.fg-editor input#${fieldID}');
    const card = inp && inp.closest('.fg-editor');
    return card ? [...card.querySelectorAll('button')].find(b => /^(保存|创建供应商|创建模型)$/.test((b.innerText||'').replace(/\\s+/g,''))) : null
  })()`
}

/**
 * 选中一个 antd Select 的某项（按**表单字段的 input id** 定位）。
 *
 * 三条路径依次尝试，每条都真实走输入管线：① 真实鼠标点下拉项；② 直接回车（showSearch 下
 * 过滤后的首项即目标）；③ ↓ + 回车（把激活项挪到第一项）。
 *
 * 为什么要这三步：**首次实跑**里合成 `el.click()`、`.trim() === '保存'`、`.ant-modal .ant-select`
 * 三种"看起来显然"的写法全是坑——antd 会插空格、DOM 顺序会先命中外层控件、
 * 合成 click 对 Select 选项不稳。这里把踩过的坑收敛成一个助手，调用点只写"选哪一项"。
 *
 * 作用域 2026-10-11 随交互改造从 `.ant-modal` 换成 `.fg-editor`（就地展开的编辑卡）：
 * 字段 id 在两块之间不重名，所以"按字段 id 找到那张卡"仍然唯一。
 */
async function selectByFieldID(page, fieldID, text) {
  const inputSel = `.fg-editor input#${fieldID}`
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
 * "终态事件 → refreshSessions" 驱动——固定 sleep 等于把断言押在模型速度上。
 */
async function waitSessionTerminal(id, timeoutMs = 90000) {
  // 终态闭集取自前端契约的 `SessionStatus`（`in_progress` = 有轮次在跑，**不是**终态）。
  const TERMINAL = new Set(['completed', 'cancelled', 'failed', 'interrupted'])
  const t0 = Date.now()
  let last = 'unknown'
  while (Date.now() - t0 < timeoutMs) {
    try {
      // api() 返回的是 { status, json }（信封在 json 里）
      const r = await api('GET', `/sessions/${id}`)
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

/**
 * 按"当前 agent"定向适用/不适用两条 skill（切换当前 agent 之后必须再调一次——角色会跟着换）。
 *
 * 定义**现算**：每个在册 agent 一条 `ui-verify-<id>`（钉它自己的 runtime）；"不适用"优先取另一个
 * **在册** agent，没有第二个时取 `ABSENT_RUNTIME` 占位（它同样要出现在清单里、要标"不可选"）。
 * 整张表里没有任何"本仓随包哪个底座"的假设，只有 `GET /agents` 的事实。
 */
function orientSkills(agents) {
  const cur = agents.find((a) => a.is_default) ?? agents[0]
  RUNTIME = cur.id
  OTHER_RUNTIME = agents.find((a) => a.id !== cur.id)?.id ?? ABSENT_RUNTIME
  const defFor = (rt) => ({ id: `ui-verify-${rt}`, name: `UI验收-仅${rt}`, runtimes: [rt] })
  SKILL_DEFS = {}
  for (const a of agents) SKILL_DEFS[a.id] = defFor(a.id)
  for (const rt of [RUNTIME, OTHER_RUNTIME]) if (!SKILL_DEFS[rt]) SKILL_DEFS[rt] = defFor(rt)
  SKILLS = { applicable: SKILL_DEFS[RUNTIME].id, inapplicable: SKILL_DEFS[OTHER_RUNTIME].id }
  return cur
}

/**
 * 「底座词汇」正则：**在册 agent 的 id** 全部算代码词（ADR-22 的文案纪律——设置 · 模型那一节的
 * 正文只许出现用户词「供应商」「模型」），外加 provider/mcp/skill/runtime 这几个代码词。
 * 按在册事实现算，不写死某个底座名。
 */
function runtimeWordRe() {
  const ids = AGENTS.map((a) => String(a.id ?? '')).filter(Boolean)
    .map((id) => id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
  return new RegExp([...ids, 'provider', 'mcp', 'skill', 'runtime'].join('|'), 'i')
}

/**
 * 把"本构建在册的 runtime"读成事实（P2）：`GET /agents` 里有什么，脚本就按什么跑。
 *
 * 为什么不从构建参数（`RUNTIME=`）读：验收要验的是**跑着的那个实例**，构建参数只是开发者的一面
 * 之词；agent 是否在册是服务端实际装配的结果（选了未注册的底座会**启动失败**，见 `ProductRegistry`）。
 */
async function resolveRuntimeFacts() {
  const res = await api('GET', '/agents')
  AGENTS = res.json.data ?? []
  if (!AGENTS.length) {
    throw new Error(`GET /agents 为空（${JSON.stringify(res.json).slice(0, 200)}）——没有在册 agent，场景无从验起`)
  }
  const cur = orientSkills(AGENTS)
  INITIAL_DEFAULT_AGENT = cur.id
  console.log(`  ℹ️  在册 agent = ${AGENTS.map((a) => `${a.id}${a.is_default ? '(当前)' : ''}`).join(', ')}；` +
    `本次靶上的适用 skill = ${SKILL_DEFS[RUNTIME].name}，不适用 = ${SKILL_DEFS[OTHER_RUNTIME].name}`)
  return AGENTS
}

/**
 * dev 态「跑的是哪一份底座」的如实报告（P4 / 设计 D6）。
 *
 * 判据来源只有一处：核心 `/meta.payloads[]`（dev 态由 `dev-loop.mjs` 用 `--payloads` 交进去，
 * 与桌面态由壳交进去的是**同一个字段**）。随包那一份从磁盘上的 `payload.json` 读——**不执行**任何
 * 底座（底座连 `--version` 都写自己的 home，探一下就是动用户环境）。
 *
 * **宿主环境面**：这里刻意不去读宿主机上装了什么（用户的底座状态目录、PATH 上那个命令、`--version`
 * 一律不碰）。语义是"**宿主机上装过什么与本产品无关**"——宿主那份既不进判据，也不是本仓的交付物；
 * 本报告只回答"这次跑的是哪一份"，回答不了就说"无从判定"。
 *
 * **刻意不是判据**：D6 的口径是「不一致时打印两者并 WARN，**不判红**」。dev 跑本机装的那份是合法
 * 选择，红在这里只会逼人把 `make dev` 关掉、把这个报告一起关掉。所以这段一律**不打 `check(...)`**：
 * 只在汇总之外打 ℹ️/⚠️，退出码不受影响（这条性质由 archtest 的 `TestDevLoopReportsRuntimeHit` 钉住）。
 */
function reportRuntimeHit(payloads) {
  const list = Array.isArray(payloads) ? payloads : []
  const root = join(REPO_ROOT, 'frontend/apps/desktop/resources/payloads')
  const bundledOf = (name) => {
    const dir = join(root, name)
    if (!existsSync(dir)) return []
    return readdirSync(dir).filter((v) => existsSync(join(dir, v, 'payload.json'))).sort()
  }

  console.log('\n=== 底座命中（打印，不判红：dev 跑本机那份是合法选择）===')
  if (list.length === 0) {
    console.log('  ℹ️  核心没报命中（/meta.payloads[] 为空）：这份核不是由 `make dev` 起的，或它还没带 `--payloads`。本次无从比较。')
    return
  }
  for (const p of list) {
    const bundled = bundledOf(p.name)
    const bundledTxt = bundled.length ? bundled.join(' / ') : '（仓库里没有这份载荷）'
    console.log(`  命中  ${p.name} ${p.version}（source=${p.source}，digest=${p.digest}）`)
    console.log(`  随包  ${p.name} ${bundledTxt}`)
    if (p.source === 'path' || p.version === 'unknown' || p.version === 'external') {
      console.log('  ℹ️  **无从判定是否同代**：命中来自 PATH / 仓库外，本仓刻意不探测版本' +
        '（底座连 `--version` 都写自己的 home）。要跑随包那一代，见 `make dev` 横幅给出的做法。')
    } else if (bundled.length && !bundled.includes(p.version)) {
      console.log(`  ⚠️  **不是同一代**：dev 命中 ${p.version}，随包是 ${bundledTxt}——` +
        'dev 上验过的行为不保证等于交付件上的行为。')
    } else {
      console.log('  ✅  与随包同一代。')
    }
  }
}

/**
 * 启动断言：**这个靶服务的必须是本地 `apps/web/dist` 那一份产物**（P2）。
 *
 * 判据是"逐字对照 + 资产可取"，不是"HTTP 200"：vite dev server 也会给 200，但它给的是从源码现编译
 * 的那一份（另有 `/@vite/client`、antd 的 `css-dev-only-*`、没有 build id）。所以：
 *   ① `${WEB_URL}/` 的 HTML 必须与本地 `dist/index.html` 逐字一致（去掉行尾空白）；
 *   ② 该 HTML 引用的哈希资产经 `${WEB_URL}` 取得到 200（证明它服务的就是这棵树，不只是长得像）；
 *   ③ `/meta.webPath` 指的也必须是这一份目录（核心与靶说的是同一件事）。
 * 任一条不成立即红——**不回落、不"退一步换个靶试试"**：靶错了，后面几十条断言验的都不是交付件。
 */
async function assertServingBuiltArtifact() {
  const localIndex = join(WEB_DIR, 'index.html')
  if (!existsSync(localIndex)) {
    check('前置：本地产物 apps/web/dist 存在', false, `缺 ${localIndex}——先构建一次（\`make web\`，或直接 \`make dev\`）`)
    return false
  }
  const want = readFileSync(localIndex, 'utf8').replace(/\s+$/, '')
  // 靶不可达要说清"下一步做什么"：本脚本**刻意不自起服务**（它验的是"人正在看的那个实例"），
  // 所以"连不上"的第一原因是 `make dev` 没在跑，而不是页面坏了。
  let res
  try {
    res = await fetch(WEB_URL)
  } catch (err) {
    throw new Error(
      `靶不可达：${WEB_URL}（${err?.cause?.code ?? err?.message ?? err}）。` +
      `本目标验的是**人正在看的那个实例**、脚本不自己起服务——先在另一个终端起 \`make dev\`，` +
      `或把 WEB_URL 指到别处（但那个靶给的必须是本地产物，否则下面的断言会红）。`)
  }
  const got = (await res.text()).replace(/\s+$/, '')
  const same = res.status === 200 && got === want
  check('靶服务的就是本地那份产物（index.html 逐字一致）', same,
    `${WEB_URL} → HTTP ${res.status}；本地 ${want.length} 字节 vs 远端 ${got.length} 字节` +
    (same ? '' : `；远端首行=${JSON.stringify(got.split('\n')[0]?.slice(0, 90))}`))

  const asset = want.match(/\/assets\/[A-Za-z0-9._-]+\.js/)?.[0]
  if (!asset) {
    check('产物 index.html 里能找到哈希资产引用', false, '没匹配到 /assets/*.js')
    return false
  }
  const assetRes = await fetch(`${WEB_URL}${asset}`)
  check('该产物的哈希资产取得到', assetRes.status === 200, `${asset} → HTTP ${assetRes.status}`)

  const meta = await api('GET', '/meta')
  const webPath = meta.json.data?.webPath
  check('/meta.webPath 指的就是这一份产物目录', String(webPath) === WEB_DIR, `webPath=${webPath}`)

  // 底座命中报告（P4/D6）：**打印，不判红**——它回答的是「这次验的是哪一代底座」，
  // 与「靶对不对」是两件事（后者红了才真的没意义）。
  reportRuntimeHit(meta.json.data?.payloads)
  return same && assetRes.status === 200
}

/** 造验收用 skill（幂等：40901 存在即算成功）：每个在册 agent 一条 + 一条不在册的不可达项。 */
async function seedSkills() {
  for (const def of Object.values(SKILL_DEFS)) {
    const r = await api('POST', '/skills', {
      id: def.id,
      name: def.name,
      description: '界面验收用（脚本自建自清）',
      body: '这是一条界面验收用的技能说明，不含任何危险指令。',
      runtimes: def.runtimes,
    })
    if (r.json.code !== 0 && !String(r.json.msg ?? '').includes('已存在')) {
      throw new Error(`建 skill ${def.id} 失败: ${JSON.stringify(r.json)}`)
    }
  }
}

async function cleanupCreated() {
  for (const id of createdSessions) {
    const r = await api('PATCH', `/sessions/${id}`, { archived: true }).catch(() => null)
    console.log(`  🧹 归档验收会话 ${id}: ${r ? r.json.code : 'n/a'}`)
  }
}

async function cleanupSkills() {
  // 按 SKILL_DEFS 清：seedSkills 造的就是这些（多 agent 时不止两条），自建自清不漏。
  for (const def of Object.values(SKILL_DEFS)) {
    const r = await api('DELETE', `/skills/${def.id}`).catch(() => null)
    console.log(`  🧹 清理 skill ${def.id}: ${r ? r.json.code : 'n/a'}`)
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
// DoD（计划 §5）：勾选后建会话 → `GET /sessions/:id` 回显选定明细与钉住的指纹；
//                不可达时前端给解释。
// ————————————————————————————————————————————————————————————————
async function scenarioCapability(browser) {
  section('M3-4 能力勾选面板（勾选 skill 子集 → 建会话 → 回显选定与指纹）')
  const page = await openPage(browser, `${WEB_URL}/agents`)
  await page.waitFor(textExpr('当前'), { label: 'Agents 页渲染（「当前」行标）' })
  await page.shot('01-agents')

  // 「界面切当前 agent」是**前提**（能力清单按 agent 视角取），不是被测对象本身：
  // 在册 agent 只有一条时（本构建的常态——底座构建期固定）这一段没有对象，**跳过而非判红**。
  const others = AGENTS.filter((a) => !a.is_default)
  if (others.length) {
    const setBtn = `[...document.querySelectorAll('button')].filter(b => (b.innerText||'').includes('设为当前'))`
    const n = await page.evaluate(`return ${setBtn}.length`)
    check('Agents 页有且仅有一个非默认 agent 的「设为当前」', n === 1, `按钮数=${n}（非默认 agent=${others.map((a) => a.id).join(',')}）`)
    await page.click(`${setBtn}[0]`)
    await page.waitFor(`return ${JSON.stringify(true)}`, { label: '等待切换落定', timeout: 3000 }).catch(() => {})
    await sleep(1200)
    const def = await api('GET', '/agents')
    AGENTS = def.json.data ?? AGENTS
    const target = others[0].id
    check(`默认 agent 已切到 ${target}（界面操作生效）`,
      AGENTS.find((a) => a.id === target)?.is_default === true,
      `agents=${AGENTS.map((a) => `${a.id}:${a.is_default}`).join(' ')}`)
    orientSkills(AGENTS)
  } else {
    console.log(`  ℹ️  /agents 只有一条在册 agent（${AGENTS[0].id}）：「界面切当前 agent」没有对象，跳过；` +
      `能力勾选面板照常验——不适用那条 skill 钉的是**不在册**的占位 runtime（${ABSENT_RUNTIME}），` +
      `它照样进清单并标"不可选"（这条判据验的是"不适用也列出来、不隐藏"，不需要第二个底座存在）`)
  }

  // 回新建页，打开「可用的能力」
  await page.goto(`${WEB_URL}/`, { waitText: '可用的能力' })
  await page.waitFor(textExpr(RUNTIME), { label: `新建卡显示当前 Agent=${RUNTIME}` })
  await page.shot(`02-start-with-${RUNTIME}`)
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
  check('自定义列表列出适用 skill', custom.includes(SKILL_DEFS[RUNTIME].name), custom.split('\n').filter((l) => l.includes('UI验收')).join(' | ').slice(0, 200))
  check('不适用 skill 也列出来（不隐藏）', custom.includes(SKILL_DEFS[OTHER_RUNTIME].name), custom.split('\n').filter((l) => l.includes('UI验收')).join(' | ').slice(0, 240))
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
    check('hover 不适用项给出原因（tooltip）', /runtimes|不相交/.test(tip) || tip.includes(OTHER_RUNTIME), tip.slice(0, 200))
  } else {
    check('hover 不适用项给出原因（tooltip）', false, '没有 .fg-cap-opt-why 元素')
  }

  // 真正勾一下（三态语义：一个都不勾 = 明确一个都不要；再勾回来 = 只剩它）
  // 勾的是**适用项**（它才会被投给会话；不适用项是 fg-off，点不动）
  const appCheckbox = `[...document.querySelectorAll('.fg-cap-opt')].find(o => (o.innerText||'').includes(${JSON.stringify(SKILL_DEFS[RUNTIME].name)}))`
  await page.click(`${appCheckbox}.querySelector('input[type=checkbox]')`)
  await sleep(350)
  const noneSel = await page.evaluate(`return document.querySelector('.fg-cap-pop').innerText`)
  check('取消勾选 → 明确表达"一个都不要"', /一个都不要/.test(noneSel), noneSel.split('\n').filter((l) => /只投/.test(l)).join(' | '))
  await page.click(`${appCheckbox}.querySelector('input[type=checkbox]')`)
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
  await page.waitFor(`return /\\/sessions\\/[0-9a-zA-Z-]+/.test(location.pathname)`, { label: '会话创建并跳转', timeout: 40000 })
  const convId = await page.evaluate('return location.pathname.split("/").pop()')
  createdSessions.push(convId)
  await sleep(1500)
  await page.shot('07-session-created')
  console.log(`  → 会话 id = ${convId}`)

  // DoD：GET /sessions/:id 回显选定明细与钉住的指纹
  const detail = await api('GET', `/sessions/${convId}`)
  const d = detail.json.data ?? {}
  console.log(`  → GET /sessions/:id 关键字段: ${JSON.stringify({ capabilities: d.capabilities, capability_fingerprint: d.capability_fingerprint, agent_id: d.agent_id, runtime: d.runtime }).slice(0, 400)}`)
  check('会话详情回显能力选定明细', JSON.stringify(d.capabilities ?? null).includes(SKILLS.applicable), JSON.stringify(d.capabilities ?? null))
  const fp = d.capabilities?.fingerprint ?? d.capability_fingerprint
  check('会话详情回显钉住的能力指纹', typeof fp === 'string' && fp.length >= 8, `fingerprint=${String(fp).slice(0, 24)}`)
  check('会话落在所选 agent 上', d.agent_id === RUNTIME, `agent_id=${d.agent_id} runtime=${d.runtime}（期望 ${RUNTIME}）`)

  // 界面侧：会话详情「能力」行
  const openDetail = await page.click(`[...document.querySelectorAll('button')].find(b => (b.getAttribute('aria-label')||b.getAttribute('title')||'') === '会话详情')`, { optional: true })
  if (openDetail) {
    await sleep(900)
    await page.shot('08-session-detail')
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

  // Agent：按在册事实选（不写死某个底座——本构建的 agent 是构建期定好的那一个）
  const selText = (id) => page.evaluate(`return (document.getElementById(${JSON.stringify(id)}).closest('.ant-select').querySelector('.ant-select-content')||{}).innerText || ''`)
  // 默认值可能已经是它：先看当前值，不是才切（切换本身会走一次下拉，值与值相同也白走）
  const beforePick = await selText('agent_id')
  if (!beforePick.includes(RUNTIME)) {
    await page.pickSelect(`document.getElementById('agent_id').closest('.ant-select')`, RUNTIME)
  }
  const agentText = await selText('agent_id')
  check(`Agent 已选到在册的那个（${RUNTIME}）`, agentText.includes(RUNTIME), `选中项=${agentText}`)

  // 工作空间：点药丸 → 取第一条候选目录（没有就手打绝对路径）→ 用药丸文案自证已选上
  const pillName = `(document.querySelector('.fg-ws-pill-name')||{}).innerText || ''`
  check('工作空间默认是「选择目录」', (await page.evaluate(`return ${pillName}`)) === '选择目录', await page.evaluate(`return ${pillName}`))
  await page.click(q('.fg-ws-pill'))
  await sleep(600)
  const manualInput = `input[placeholder^="输入绝对路径"]`
  const wsItems = await page.count('.fg-ws-item')
  // 菜单里是**候选区**（当前 + 已登记的工作空间 + 添加工作空间…）。
  // 手输绝对路径**不在这个菜单里**——v14.3 / #41 起它在目录浏览视图（`DirectoryBrowser` 的面包屑编辑态）。
  // 老断言写的是"候选区与手输入口同屏"，于是它必然红；2026-10-11 之前 `auto` 在更早的 agent 那步就
  // 超时中断了，**这条断言根本没机会跑**，所以这处过时直到脚本能跑完才暴露出来。
  check('工作空间选择器打开（有候选区）', wsItems > 0, `候选条目=${wsItems}`)
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
  await page.waitFor(`return location.pathname.startsWith('/sessions/')`, { label: '手动运行后跳到会话', timeout: 90000 })
  const autoConv = await page.evaluate('return location.pathname.split("/").pop()')
  createdSessions.push(autoConv)
  // 等这一轮**真正结束**再断言来源标：侧栏的 `origin` 标由"终态 → refreshSessions"带出
  // （列表是轮询刷新的，而事件流里没有 origin），固定 sleep 会在模型慢时假红——
  // 实测同一次运行：断言时无标，+5s 后 status=completed 且标出现。
  const st = await waitSessionTerminal(autoConv, 120000)
  check('自动化触发的会话跑到终态（等标的前提）', st !== 'timeout', `status=${st}`)
  await page.shot('16-automation-session')

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
  const convDetail = await api('GET', `/sessions/${autoConv}`)
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
  check('运行史记录里有会话可回溯', recs.some((r) => r.session_id), (recs.map((r) => r.session_id).filter(Boolean)[0] ?? '无'))

  // 工作空间「手输绝对路径」那条路（v14.3 / #41 的形状）：它是**另一个页面**上验的——这一段会开关一个
  // 模态对话框，而 Esc/取消在嵌套模态下会连带关掉外层那个（实测：在自动化弹窗里试，表单当场被关掉，
  // 后面 6 条断言连环红）。所以另开一个 tab，验完就丢，碰不到上面这条主流程。
  //
  // **先读服务端声明再决定点不点**：本机核（绑回环 / 非 SSH / 有显示会话）会声明 `native-dialog` 可用，
  // 于是「添加工作空间…」抬的是**系统面板**（`POST /workspaces/pick-directory` → 核心 spawn osascript），
  // 不是应用内浏览——自动验收只能在 browse 那一态上跑，这种机型上**显式跳过**（同 `wsdir` 场景的
  // 前置：给核的进程环境一个 `SSH_CONNECTION` 才能降到 browse）。硬点会弹出真人面板。
  const capsRes = await api('GET', '/capabilities')
  const nativeDialog = (capsRes.json.data?.capabilities ?? []).find((c) => c.id === 'native-dialog')?.available === true
  if (nativeDialog) {
    skip('工作空间手输路径（应用内浏览那一路）',
      '本机核声明 native-dialog 可用 →「添加工作空间…」抬的是系统面板、要真人点；' +
      '自动验收只能在 browse 态上跑：给核的进程环境一个 SSH_CONNECTION（见 `pnpm ui:accept wsdir`）')
  } else {
    const p2 = await openPage(browser, `${WEB_URL}/`)
    await p2.waitFor(textExpr('可用的能力'), { label: '新建页就绪（工作空间药丸在那儿）' })
    await p2.click(q('.fg-ws-pill'))
    await sleep(700)
    const addEntry = `[...document.querySelectorAll('.fg-ws-item')].find(o => (o.innerText||'').includes('添加工作空间'))`
    if (await p2.box(addEntry)) {
      await p2.click(addEntry)
      await sleep(900)
      await p2.shot('12a-workspace-browse')
      const dlg = await p2.count('.fg-ws-dlg-body')
      check('「添加工作空间…」进目录浏览视图', dlg === 1, `对话框体=${dlg}`)
      // 手输框**不是**对话框一开就有的：它在面包屑的编辑态里（点铅笔 `.fg-ws-crumbedit` 才出现）。
      // 老断言正是在菜单里找它、且假设与候选区同屏——两处都过时了，而它此前一直没机会跑到。
      const crumbEdit = `document.querySelector('.fg-ws-crumbedit')`
      if (await p2.box(crumbEdit)) await p2.click(crumbEdit)
      await sleep(400)
      const hasManual = (await p2.box(q(manualInput))) !== null
      check('面包屑编辑态给出「输入绝对路径」框', hasManual, `手输入口=${hasManual}`)
      if (hasManual) {
        // 路径走服务端校验，必须落在 MATE_WORKSPACE_ROOTS 内（`/tmp` 会被正当拒掉）
        const roots = await api('GET', `/workspaces/browse?path=${encodeURIComponent(process.env.HOME + '/.ark-mate/workspaces')}`)
        const dir = roots.json.data?.path
        if (dir) {
          await p2.type(q(manualInput), dir)
          await p2.press('Enter')
          await sleep(1000)
          // 编辑态收起 = 已经跳到那一层；路径非法时输入框会留着（并弹 toast）
          const stillEditing = (await p2.box(q(manualInput))) !== null
          check('手输绝对路径后跳到那一层（编辑态收起）', !stillEditing, `path=${dir} 编辑态仍在=${stillEditing}`)
          await p2.shot('12b-workspace-browse-typed')
        }
      }
    } else {
      check('菜单里有「添加工作空间…」入口', false, '没找到（memory 持久层时该项整项不出现——本场景要求 sqlite）')
    }
  }

  return { page }
}

// ————————————————————————————————————————————————————————————————
// 场景三：M5′ 命令发现面（**本构建跳过**）
// 依据：ADR-17 / README。命令通告是 ACP 的**运行时能力**：只有会通告命令的 adapter 建出的会话才
//       可能收到 `session.commands`，面板输入框打 `/` 才给候选；事件侧证据 = history 里有该事件。
//
// 历史实测（2026-10-11）：这条场景此前一直"靠残留数据变绿"——它从历史会话里挑一条本脚本早先几轮
// 自己创建、属于某个**当时在册、如今已不在仓库**的 adapter 的会话，于是"验过了"其实什么也没验；
// 而在干净实例上它会红在 `POST /sessions {agent_id:'<那个 adapter>'}` 上（那个 agent 根本没注册）。
//
// 本仓现在只装配一个 adapter（`AllBuiltinSpecs()` 只返回它），`GET /agents` 的在册事实里没有任何
// adapter 以"通告命令"为已实测能力，所以这条场景**无从验起**：显式跳过并写明理由——既不拿历史
// 残留数据充当绿，也不把"本构建没有这个能力"判成红。
// ————————————————————————————————————————————————————————————————
async function scenarioCommands() {
  section('M5′ 命令发现面（底座通告 → 输入框 `/` 候选 + 事件侧证据）')
  skip('cmd: 命令发现面',
    `本构建在册的 runtime = ${AGENTS.map((a) => a.id).join('/')}；在册 adapter 没有任何一条以` +
    '"通告命令"（`session.commands`，ADR-17 的 ACP 运行时能力）为已实测能力，这条场景无从验起')
  return {}
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
// v14.3 目录浏览对话框（对齐 dsh 的 DirectoryBrowser）——真 Chrome + 真核 + 真文件系统。
//
// 覆盖的判据（设计 §5 的 G1/G2/G3），逐条：
//   ① 打开是**对话框**，头部是**面包屑**且白名单下从最近的根起（根外的祖先名字不该出现在界面上）；
//   ② 路径一行给**绝对路径全文**（面包屑会被长路径挤到只剩尾段，全文才是"我在磁盘哪儿"的答案）；
//   ③ 点一行 → **双栏**：该行高亮、右栏列出它的子目录（Miller 视图）；
//   ④ 「显示隐藏文件」是开关：打开出现隐藏目录、关掉消失（服务端按 `hidden` 过滤）；
//   ⑤ 「新建文件夹」经 `POST /workspaces/dirs` **真建出目录**，并落到该层选中它；
//   ⑥ 确认动作（「加入白名单」）把**选中的那个目录**交给调用方，保存后服务端 configured 里真有它
//      ——即"选目录"这条链路的写面闭环。
//
// 夹具（自建自清，全部在 `mkdtempSync(tmpdir())` 下）：场景开始时把 `MATE_WORKSPACE_ROOTS` 临时设成
// 夹具根，结束时**还原**（先备份、finally 恢复）——白名单是这台机器上真核的配置，借了必须还。
//
// 为什么值得单列一个场景：这一节的价值全在"界面真的长出来了"——面包屑 / 双栏 / 开关 / 新建文件夹
// 四样都是 REST 冒烟看不出的形态；而「新建文件夹」还是全仓唯一往磁盘写新文件的界面动作。
//
// **跑之前要在核的进程环境里给一个 `SSH_CONNECTION`**（如 `SSH_CONNECTION=1 pnpm ...`）：宿主选择器
// 的判定是"绑回环 + 非 SSH + 有显示会话"，而验收要在**应用内浏览**那一态上跑（系统面板需要真人回答，
// 自动跑不了）。给了这个键，判定确定性地落在 browse 上，第 ⑦ 步顺带把它钉住。
async function scenarioWorkspaceDir(browser) {
  section('v14.3 目录浏览对话框（面包屑 / Miller 双栏 / 隐藏开关 / 新建文件夹 / 白名单闭环）')

  // realpath 一次：macOS 上 /var 与 /private/var 是同一目录，而服务端一律回**真身**路径——
  // 不解析的话，下面每一条"路径相等"的断言都会因为写法差异而假红。
  const fixture = realpathSync(mkdtempSync(join(tmpdir(), 'mate-wsdir-')))
  for (const d of [WSDIR.keep, join(WSDIR.keep, 'src'), WSDIR.hidden]) {
    mkdirSync(join(fixture, d), { recursive: true })
  }
  writeFileSync(join(fixture, WSDIR.file), 'ui-verify\n')

  const before = await api('GET', '/settings/workspace')
  const previousRoots = before.json.data?.configured ?? []
  const previousLocked = before.json.data?.locked_by ?? ''
  if (previousLocked) {
    check('白名单未被 env/flag 锁定（否则本场景无处借位）', false, `locked_by=${previousLocked}`)
    rmSync(fixture, { recursive: true, force: true })
    return {}
  }

  let page
  try {
    await api('PUT', '/settings/workspace', { roots: [fixture] })

    page = await openPage(browser, `${WEB_URL}/`)
    await page.waitFor(textExpr('设置'), { label: '左栏渲染' })
    await page.click(btnExpr('设置'))
    await page.waitFor(`return !!document.querySelector('.fg-setdlg')`, { label: '设置弹窗打开' })
    await page.click(`[...document.querySelectorAll('.fg-setdlg-item')].find(b => (b.innerText||'').includes('通用'))`)
    await page.waitFor(textExpr('工作区目录白名单'), { label: '通用那一节渲染' })
    await sleep(500)
    await page.shot('60-settings-general')

    // ① 打开对话框（设置页的「浏览…」与新建会话的选择器共用同一份实现）
    await page.click(btnExpr('浏览…'))
    await page.waitFor(`return !!document.querySelector('.fg-ws-dlg')`, { label: '目录对话框打开' })
    await sleep(900)
    await page.shot('61-directory-dialog')

    const pathLine = await page.evaluate(`return (document.querySelector('.fg-ws-dlg-path')||{}).innerText || ''`)
    const crumbs = await page.evaluate(`return [...document.querySelectorAll('.fg-ws-crumb')].map(c => (c.innerText||'').trim())`)
    check('起于偏好起点（路径一行 = 绝对路径全文）', pathLine === fixture, pathLine)
    // v14.5：名录降级为"起点偏好"，面包屑**不再裁前缀**——给到文件系统根的完整祖先链。
    check('面包屑是完整祖先链（首项是文件系统根）',
      crumbs.length === fixture.split('/').length - 1 + 1 && crumbs[0] === '/' && crumbs[crumbs.length - 1] === basename(fixture),
      JSON.stringify(crumbs))

    // ② 单栏 + 只列非隐藏目录；文件只读展示
    const rows0 = await page.evaluate(`return [...document.querySelectorAll('.fg-ws-col .fg-ws-item')].map(b => (b.innerText||'').trim())`)
    check('单栏起步且隐藏目录不在列表里',
      (await page.count('.fg-ws-col')) === 1 && !rows0.includes(WSDIR.hidden), JSON.stringify(rows0))
    check('文件只读展示（不进可进入的行）', (await page.text()).includes(WSDIR.file), '')
    // v14.5：偏好起点不再是"不许上去的边界"，所以「上级」在起点处**可用**。
    check('起点处「上级」可用（名录不再是边界）',
      await page.evaluate(`const b = [...document.querySelectorAll('.fg-ws-icon-btn')].find(x => x.title === '上级目录'); return !!b && !b.disabled`), '')

    // ③ 点一行 → 双栏（该行高亮 + 右栏 = 它的 children）
    const keepRow = `[...document.querySelectorAll('.fg-ws-col .fg-ws-item')].find(b => (b.innerText||'').includes(${JSON.stringify(WSDIR.keep)}))`
    await page.click(keepRow)
    await sleep(1000)
    const cols = await page.evaluate(`return document.querySelectorAll('.fg-ws-col').length`)
    const rightRows = await page.evaluate(
      `const cols = document.querySelectorAll('.fg-ws-col'); if (!cols[1]) return []; return [].map.call(cols[1].querySelectorAll('.fg-ws-item'), (b) => (b.innerText||'').trim())`,
    )
    check('点一行后变双栏且该行高亮',
      cols === 2 && (await page.evaluate(`return !!document.querySelector('.fg-ws-rowsel')`)), `cols=${cols}`)
    check('右栏列出选中项的子目录', JSON.stringify(rightRows) === JSON.stringify(['src']), JSON.stringify(rightRows))
    await page.shot('62-two-pane')

    // ④ 隐藏项开关（两向）
    await page.click(q('.fg-ws-toggle'))
    await sleep(1000)
    check('打开「显示隐藏文件」后隐藏目录出现', (await page.text()).includes(WSDIR.hidden), '')
    await page.shot('63-hidden-on')
    await page.click(q('.fg-ws-toggle'))
    await sleep(1000)
    check('关掉后隐藏目录重新消失', !(await page.text()).includes(WSDIR.hidden), '')

    // ⑤ 新建文件夹：建在**选中项**下，落点即它
    await page.click(keepRow)
    await sleep(900)
    await page.click(btnExpr('新建文件夹'))
    await page.waitFor(`return !!document.querySelector('.fg-ws-create input')`, { label: '新建文件夹对话框' })
    await page.type(`document.querySelector('.fg-ws-create input')`, WSDIR.created)
    await page.click(btnExpr('创建'))
    await sleep(1600)
    check('新建的目录出现在列表里（POST /workspaces/dirs 真的建出来了）',
      (await page.text()).includes(WSDIR.created) && existsSync(join(fixture, WSDIR.keep, WSDIR.created)),
      join(fixture, WSDIR.keep, WSDIR.created))
    await page.shot('64-created')

    // ⑥ 确认：加入起点偏好 → 保存 → 服务端 configured 里真有它
    await page.click(btnExpr('加入起点'))
    await sleep(1500)
    check('「加入起点」把**选中的那个目录**放进待保存列表', (await page.text()).includes(WSDIR.created), '')
    await page.shot('65-adopted')
    // 重试是脚本对"切弹窗后首点可能落在未完成重渲染的那一帧"的容错，不是产品行为。
    for (let i = 0; i < 3; i++) {
      await page.click(btnExpr('保存起点'))
      await sleep(1500)
      const saved = await api('GET', '/settings/workspace')
      if ((saved.json.data?.configured ?? []).some((p) => p.endsWith(WSDIR.created))) break
    }
    const saved = await api('GET', '/settings/workspace')
    check('保存后服务端 configured 含新目录（设置面闭环）',
      (saved.json.data?.configured ?? []).some((p) => p.endsWith(WSDIR.created)),
      JSON.stringify(saved.json.data?.configured))
    await page.shot('66-saved')
    // ⑦ 两态编排的**判定源**（v14.5）：形态由服务端声明（`/capabilities` 的 `native-dialog`），
    //    不是客户端嗅探。本场景的核是在 SSH 环境里起的（见文件头的 wsdir 说明），所以判定必然是
    //    browse —— 上面六步走的正是"应用内浏览"那一态，这一步把"判定确实来自服务端"钉住。
    const caps = (await api('GET', '/capabilities')).json.data
    const nativeDialog = (caps?.capabilities ?? []).find((c) => c.id === 'native-dialog')
    check('能力声明里 native-dialog 存在且有 browserFallback',
      !!nativeDialog && nativeDialog.browserFallback === 'server:directory-browse',
      JSON.stringify(nativeDialog))
    check('SSH 会话下宿主判定为 browse（本机浏览器/桌面窗口之外不抬系统面板）',
      nativeDialog?.available === false, `available=${nativeDialog?.available}`)

  } finally {
    // 环境还原 + 夹具清理：白名单先还回去（它是这台机器上真核的配置），再删临时树。
    await api('PUT', '/settings/workspace', { roots: previousRoots })
    rmSync(fixture, { recursive: true, force: true })
  }
  return { page }
}

// ————————————————————————————————————————————————————————————————
// v11「设置 · 模型」那一节（A11）：两个平级的管理对象 + 生效状态列 + 文案纪律。
//
// 覆盖的判据（计划 A11，逐条）：
//   ① 开设置 → 切到「模型」→ 两块标题是「供应商」「模型」，且**不出现 `provider` 做标题**；
//   ② 供应商：新建凭据 → 填密钥 → 自检（只跑本地段，零出网）→ 删除；
//   ③ 模型：新建 → 改显示名 / 隐藏 / 置顶 → 点星号设默认 → 生效状态列有值 → 删除；
//   ④ **文案纪律**：保存成功的提示只能说「已保存，将在下一个会话进程生效」，不许说「已生效」。
//
// 全部用 REST 收尾（`finally`），跑完不留夹具。
//
// **本场景 2026-10-10 已在真 Chrome（headless）+ `make dev` 上跑绿，随后并进默认四场景**（见文件头）：
// 仍保留可单独执行的一条（`pnpm ui:accept model`），只改这一节时用它快速回归。
// 2026-10-13 修掉它自己的一处时序竞态：原来用固定 `sleep` 赌"上一张卡的写面已返回"，赌不赢就会
// 去开下一张卡而按钮还是 `disabled`——红的样子（元素凭空消失）与页面本身毫无关系，见 `waitEditorGone`。
async function scenarioSettingsModel(browser) {
  section('v12 设置 · 模型（供应商 + 模型两块 / 单一默认 / 聚合生效结论 / 零底座词汇）')
  // 这一节的点击分两类，依据是**哪条路真的走得通**（2026-10-13 实测）：
  //  - "开卡 / 切卡"这类（新建供应商、编辑、添加模型、切设置分节、勾能力）：JS 触发（`clickJS`）。
  //    它们在入场动画 / 重排期间按坐标点会落在动画中的位置或浮层上——不报错、也永不生效，
  //    随后以「等某个元素超时」或「标签没变」的样子出现（一次假红，原因完全看不见）。
  //  - "保存"：保留 CDP 真实鼠标。合成 `el.click()` 对受控弹窗里的 antd Button **不总响应**
  //    （下面供应商创建那段有三条失败路径的完整记录），而那正是本场景要验的写面入口。
  // 两类都不削弱判据：断言读的始终是**点完之后的状态**（DOM 读面 + REST 读面双重断言）。
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
    // **用 JS 触发，不按坐标点**（2026-10-13 实测）：弹窗刚出现时还在 antd 的入场动画里——
    // 上面那条 `waitFor` 只断言"有 client rect"，动画第一帧就通过，此时按坐标点很可能落在
    // 动画中的位置或浮层上，**点了等于没点**。表现不是"点不到元素"（坐标是能算出来的），而是
    // "设置弹窗开着、右侧一直是别的那一节"，随后等 `.fg-model-panel` 超时——一次假红，
    // 且原因完全看不见。JS 触发不过命中测试，动画/浮层都影响不到它，所以这里刻意换掉。
    const navSwitched = await clickJS(page, `[...document.querySelectorAll('.fg-setdlg-item')].find(b => (b.innerText||'').includes('模型'))`)
    check('设置弹窗里切到「模型」那一节（JS 触发，菜单项真的被按下）', navSwitched === true, String(navSwitched))
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
    check('表头标题不含代码词', titles.length >= 2 && !titles.some((t) => runtimeWordRe().test(t)), JSON.stringify(titles))

    // ② 供应商：新建 → 自检（本地段）→ 留在列表里
    await clickJS(page, btnExpr('新建供应商'))
    await page.waitFor(`return !!document.querySelector('.fg-editor input#id')`, { label: '供应商编辑卡展开' })
    await page.type('document.querySelector(".fg-editor input#id")', provID)
    check('供应商卡的 id 已填进去',
      (await page.evaluate(`return document.querySelector('.fg-editor input#id')?.value`)) === provID,
      String(await page.evaluate(`return document.querySelector('.fg-editor input#id')?.value`)))
    // 协议是 antd Select（不是裸 input）：**断言它开箱就是 openai**，不去手动选。
    //
    // 为什么改（2026-10-10）：手动走一遍 `selectByFieldID`（点开 → 输入 → 点选项）会在
    // 这里把表单值弄丢（实测：走完那三步后 `validateFields` 仍报「协议必填」），
    // 而"默认就是 openai"本来就是产品该有的形态——`Form` 的 `initialValues` 提供它。
    // 断言默认值比"能手动选"更强：它直接钉住那条初值链路。
    const protoShown = await page.evaluate(
      `const i = document.querySelector('.fg-editor input#protocol'); return i ? (i.closest('.ant-select')?.innerText || '') : ''`,
    )
    check('供应商卡的「协议」开箱即 openai（表单初值链路生效）',
      String(protoShown).includes('openai'), JSON.stringify(protoShown))
    // 关掉 Select 的下拉浮层：它盖在表单上，会让随后的 `page.click(input)` 落到浮层上
    // （真实鼠标事件被遮住 = 输入框根本没聚焦，insertText 打进空气里）。
    //
    // **不要用 Escape**：antd 的 Modal 默认 `keyboard`，那个按键会连带把**整个弹窗**关掉
    // （实测：之后 form 填得好好的、点保存什么也不发生）。改成点一下弹窗标题这个中性区域。
    await page.click(`document.querySelector('.ant-modal .ant-modal-title')`, { optional: true })
    await sleep(250)
    await page.type('document.querySelector(".fg-editor input#base_url")', 'https://api.deepseek.com/v1')
    await page.type('document.querySelector(".fg-editor input#api_key")', 'sk-ui-verify')
    const filled = await page.evaluate(`return JSON.stringify({
      id: document.querySelector('.fg-editor input#id')?.value,
      base_url: document.querySelector('.fg-editor input#base_url')?.value,
      api_key: (document.querySelector('.fg-editor input#api_key')?.value || '').length,
    })`)
    check('供应商卡的端点与密钥已填进去', /"base_url":"https:\/\/api\.deepseek\.com\/v1"/.test(filled) && /"api_key":[1-9]/.test(filled), filled)
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
    if (!saveRect) throw new Error('找不到供应商卡里的提交按钮：' + saveBtnExpr('base_url'))
    if (saveRect.disabled) throw new Error('供应商卡里的提交按钮被禁用：' + saveRect.disabled)
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
        `return [...document.querySelectorAll('.fg-editor .ant-form-item-explain-error')].map(e => e.innerText).join(' | ')`,
      )
      const btns = await page.evaluate(
        `return [...document.querySelectorAll('.fg-editor button')].map(b => (b.innerText||'').trim()).filter(Boolean).join(',')`,
      )
      throw new Error('供应商没出现在列表里：toast=' + toasts + ' 表单=' + filled +
        ' 校验错误=' + errs + ' 按钮=' + btns + ' 库里=' + JSON.stringify(ids.map((p) => p.id)) +
        ' 页面错误=' + (page.errors || []).join(' || '))
    }
    check('供应商创建后出现在列表里（行卡，不再是表格行）', true, provID)
    // 行卡给的是**身份 + 密钥状态点**（改造后不再有"端点"这一列：端点只在编辑卡里）。
    // 缺端点那条路径改由行卡上的「缺端点」标签承担，由 providers 的 problems 单测覆盖。
    const rowInfo = await page.evaluate(`return (() => {
      const li = [...document.querySelectorAll('.fg-model-panel section[aria-label="供应商"] .fg-rowcard')]
        .find(x => (x.innerText||'').includes(${JSON.stringify(provID)}));
      if (!li) return null;
      const dot = li.querySelector('.fg-keydot');
      return JSON.stringify({ text: (li.innerText||'').replace(/\\n/g,'|').slice(0, 200), key: dot ? dot.getAttribute('aria-label') : null });
    })()`)
    check('供应商行卡给出身份与密钥状态点', /API 密钥已配置/.test(rowInfo ?? ''), String(rowInfo).slice(0, 240))
    // 保存成功那一行是 `role="status"`，且**只说已保存、不说已生效**（ADR-22）。
    const savedLine = await page.evaluate(
      `const el = document.querySelector('.fg-model-panel .fg-savedline'); return JSON.stringify({ role: el ? el.getAttribute('role') : null, text: el ? (el.innerText||'').trim() : '' })`,
    )
    check('保存成功留下 role=status 提示行（不是飘走的 toast）',
      /"role":"status"/.test(savedLine) && /已保存/.test(savedLine) && !/已生效/.test(savedLine), savedLine)
    await page.shot('21-settings-provider-created')

    // 自检：只跑本地段（**绝不点那条会出网的**）。按钮与结果都限定在**这条供应商**的行卡里，
    // 否则多供应商时"第一个自检按钮"点的是别人。
    const probeRowExpr = `(() => {
      const li = [...document.querySelectorAll('.fg-model-panel section[aria-label="供应商"] .fg-rowcard')]
        .find(x => (x.innerText||'').includes(${JSON.stringify(provID)}));
      return li || null })()`
    const probeBtnExpr = `(() => {
      const li = ${probeRowExpr};
      return li ? [...li.querySelectorAll('button')].find(b => (b.innerText||'').replace(/\\s+/g,'') === '自检') : null })()`
    const clicked = await clickJS(page, probeBtnExpr)
    const opened = await page.waitFor(`return !!(${probeRowExpr})?.querySelector('.fg-probe')`, { label: '自检结果展开', timeout: 40000 }).catch(() => false)
    if (!opened) {
      const toasts = await page.evaluate(`return [...document.querySelectorAll('.ant-message-notice-content')].map(e => e.innerText).join(' | ')`)
      throw new Error(`自检结果没展开：clicked=${clicked} toast=${toasts}`)
    }
    await sleep(500)
    const probe = await page.evaluate(`return (${probeRowExpr}).querySelector('.fg-probe').innerText`)
    // v12 的本地段检查项：exists / protocol / base_url / secret / refs / delivery / catalog
    check('自检逐条给出结论（含端点与密钥两项）',
      /exists/.test(probe) && /base_url/.test(probe) && /secret/.test(probe),
      probe.split('\n').slice(0, 4).join(' | ').slice(0, 240))
    check('自检有「连一次上游」的显式入口（默认不出网）', /连一次上游/.test(probe), probe.slice(0, 160))

    // ②′ 供应商编辑卡（就地展开）：打开时**把当前值写进表单** → 改备注 → 保存。
    //
    // 为什么这一步不能省：新建与编辑**共用同一张卡、同一个 Form 实例**（`useForm` 的 store 活在
    // React 树之外）。新建时"写进去的值"与 `initialValues` 恰好一致，会把这条链路的毛病盖住；
    // 只有**编辑**才真的依赖"打开时同步写值"那一段（2026-10-11 的三次实测都栽在这上面，
    // 症状是编辑卡整个是空的，于是"改备注"要么什么都不发生、要么按空表单把声明改掉）。
    const editProvBtnExpr = `(() => {
      const li = ${probeRowExpr};
      return li ? [...li.querySelectorAll('button')].find(b => (b.innerText||'').replace(/\\s+/g,'') === '编辑') : null })()`
    await waitEditorGone(page, '供应商新建卡收起')
    await clickJS(page, editProvBtnExpr)
    await page.waitFor(`return !!document.querySelector('.fg-editor input#id')`, { label: '供应商编辑卡展开', timeout: 8000 })
    await sleep(300)
    const prefilled = await page.evaluate(`return JSON.stringify({
      id: document.querySelector('.fg-editor input#id')?.value,
      base_url: document.querySelector('.fg-editor input#base_url')?.value,
      idDisabled: !!document.querySelector('.fg-editor input#id')?.disabled,
    })`)
    check('编辑卡把当前值写进了表单（端点回填、id 只读）',
      /"id":"ui-verify-prov"/.test(prefilled) && /api\.deepseek\.com/.test(prefilled) && /"idDisabled":true/.test(prefilled),
      prefilled)
    await page.type('document.querySelector(".fg-editor input#note")', '验收备注')
    await page.click(saveBtnExpr('id'))
    await sleep(600)
    const patchedProv = ((await api('GET', '/providers')).json.data ?? []).find((p) => p.id === provID)
    check('供应商编辑提交后读面回带新备注', patchedProv?.note === '验收备注', JSON.stringify(patchedProv?.note))

    // ③ 模型：新建 → 出现在清单里（v12 的表单字段是 vendor / model_id）
    //
    // 改造后模型按**供应商分组**，每组头右侧一个「添加模型」入口。这里必须点**我们这个夹具供应商**
    // 那一组的入口——否则新建卡预选的是别的供应商（选择器能改回来，但那就测不到"分组入口预选对了"）。
    const groupExpr = `(() => {
      const g = [...document.querySelectorAll('.fg-model-panel section[aria-label="模型"] .fg-group')]
        .find(x => (x.querySelector('.fg-grouphead')?.innerText || '').includes(${JSON.stringify(provID)}));
      return g || null })()`
    const groupAddExpr = `(() => {
      const g = ${groupExpr};
      return g ? [...g.querySelectorAll('.fg-grouphead button')].find(b => (b.innerText||'').replace(/\\s+/g,'').includes('添加模型')) : null })()`
    const groupHeads = await page.evaluate(
      `return [...document.querySelectorAll('.fg-model-panel section[aria-label="模型"] .fg-grouphead')].map(g => (g.innerText||'').replace(/\\n/g,' ').trim())`)
    check('模型按供应商分组（组头带供应商名或 id）', groupHeads.some((g) => g.includes(provID)), JSON.stringify(groupHeads).slice(0, 240))
    await waitEditorGone(page, '供应商编辑卡收起')
    const openedModel = await clickJS(page, groupAddExpr)
    const modelCardOpen = await page
      .waitFor(`return !!document.querySelector('.fg-editor input#model_id')`, { label: '模型编辑卡展开', timeout: 8000 })
      .catch(() => false)
    if (!modelCardOpen) {
      throw new Error('模型编辑卡没展开：clickJS=' + openedModel + ' 组头=' + JSON.stringify(groupHeads))
    }
    const provPick = await selectByFieldID(page, 'vendor', provID)
    check('模型卡的「供应商」已选中该供应商', provPick.ok, provPick.detail)
    await page.type('document.querySelector(".fg-editor input#model_id")', modelID)
    check('模型卡的 model_id 已填进去',
      (await page.evaluate(`return document.querySelector('.fg-editor input#model_id')?.value`)) === modelID,
      await page.evaluate(`return JSON.stringify({
        v: document.querySelector('.fg-editor input#model_id')?.value,
        html: document.querySelector('.fg-editor input#model_id')?.outerHTML.slice(0, 160),
        ids: [...document.querySelectorAll('.fg-editor input')].map(i => i.id),
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
        ' 表单里的 model_id=' + await page.evaluate(`return document.querySelector('.fg-editor input#model_id')?.value`) +
        ' 库里=' + JSON.stringify(list.map((m) => m.model_id)))
    }
    await sleep(400)
    await page.shot('22-settings-model-created')

    // ③′ 输入模态（v13 / M6，ADR-12）：默认只收文本，且**能一键改成图片**。
    //     两处判据：行卡上那个可点的能力标签（用户看得见、一步改得动）+ REST 读面的
    //     `input_modalities`（真的落库）。改造后它从"表格单元格里的 Tag"变成"行卡上的按钮"，
    //     但**一键**这个性质被刻意保留（默认 `text` = 图片不会被接受，多两次点击会显著降低
    //     用户改对它的概率；见 console 里的注释）。
    const capTagExpr = `(() => {
      const li = [...document.querySelectorAll('.fg-model-panel section[aria-label="模型"] .fg-rowcard')]
        .find(x => (x.innerText || '').includes(${JSON.stringify(modelID)}));
      if (!li) return null;
      const b = [...li.querySelectorAll('button.fg-rowtag')].find(t => /^(文本|文本\\+图片)$/.test((t.innerText||'').trim()));
      return b || null })()`
    const capTagText = `const b = ${capTagExpr}; return b ? (b.innerText||'').trim() : ''`
    const modBefore = await page.evaluate(capTagText)
    check('模型行卡默认显示「文本」（= 图片不会被接受）', modBefore === '文本', JSON.stringify(modBefore))
    // 注意 `clickJS` 的表达式必须**求值成元素**（它自己做 `el.click()`）：早先这里写成
    // 返回布尔的 IIFE，第一次真跑就抛 `el.click is not a function`——该场景 2026-10-11 才第一次被执行。
    const tagClicked = await clickJS(page, capTagExpr)
    if (!tagClicked) throw new Error('模型行卡里找不到「文本」能力标签，点不动')
    // 等界面跟上（写面提交后清单会重载，别用固定 sleep 赌它）。
    let modAfter = ''
    // 预算 15s：写面提交后清单会重载，而这一节的重载可能**拉起底座**（2026-10-13 实测
    // `/models/catalog` 单次 3.1s），原来 3.75s 的预算在这种机器上会把「慢」判成「没生效」。
    for (let i = 0; i < 60 && modAfter !== '文本+图片'; i++) {
      await sleep(250)
      modAfter = await page.evaluate(capTagText)
    }
    check('一键切换后能力标签变成「文本+图片」', modAfter === '文本+图片', JSON.stringify(modAfter))
    const modRow = ((await api('GET', '/models')).json.data?.models ?? []).find((m) => m.model_id === modelID)
    check('REST 读面回带 input_modalities=["text","image"]',
      JSON.stringify(modRow?.input_modalities) === JSON.stringify(['text', 'image']),
      JSON.stringify(modRow?.input_modalities))
    await page.shot('22b-settings-model-modalities')

    // ③″ 就地展开的编辑卡**不许静默改掉**已声明的输入能力（2026-10-11 修的回归判据）。
    //     故障形态：`ModelFormValues` 声明了能力键、`submitModel` 也读它，但卡片里没有对应的
    //     `Form.Item` ⇒ `validateFields()` 不返回未注册字段 ⇒ 恒为 undefined ⇒ **只改备注**
    //     也会把图片能力改回仅文本，而提示只说"已保存"。
    //     所以这里刻意只改备注，然后要求读面仍是 ["text","image"]。
    //
    // 作用域一律按**字段所在的编辑卡**取（供应商卡与模型卡可能同时开着；而两块的角色不同，
    // "第一个 保存 按钮"这种写法一定会在某次改版后点错——当年的实测就是这么撞上的）。
    const cardExpr = (fieldID) => `(() => {
      const inp = document.querySelector('.fg-editor input#${fieldID}');
      return inp ? inp.closest('.fg-editor') : null })()`
    const cardField = (fieldID, sel) => `(() => {
      const c = ${cardExpr(fieldID)};
      return c ? c.querySelector(${JSON.stringify(sel)}) : null })()`
    const modelCardField = (id) => cardField('model_id', `input#${id}`)
    const modelCardSave = `(() => {
      const c = ${cardExpr('model_id')};
      return c ? [...c.querySelectorAll('button')].find(b => /^(保存|创建模型)$/.test((b.innerText||'').replace(/\\s+/g,''))) : null })()`
    const capWrapperExpr = `(() => {
      const c = ${cardExpr('model_id')};
      if (!c) return null;
      return [...c.querySelectorAll('[class*="ant-checkbox-wrapper"]')]
        .find(x => (x.innerText||'').trim() === '图片') || null })()`
    const editRowExpr = `(() => {
      const li = [...document.querySelectorAll('.fg-model-panel section[aria-label="模型"] .fg-rowcard')]
        .find(x => (x.innerText||'').includes(${JSON.stringify(modelID)}));
      return li ? [...li.querySelectorAll('button')].find(b => (b.innerText||'').replace(/\\s+/g,'') === '编辑') : null })()`
    await waitEditorGone(page, '模型新建卡收起')
    await clickJS(page, editRowExpr)
    await page.waitFor(`return !!(${cardExpr('model_id')})`, { label: '编辑模型卡片展开', timeout: 8000 })
    await sleep(400)
    const capChecked = await page.evaluate(`const w = ${capWrapperExpr}; return w ? !!w.querySelector('input')?.checked : null`)
    check('编辑模型时「图片」按当前声明回填（表单读得到能力）', capChecked === true, JSON.stringify(capChecked))
    await page.type(modelCardField('note'), '备注改动')
    await page.click(modelCardSave)
    await waitEditorGone(page, '编辑卡收起（这条编辑的写面已返回）')
    const afterEdit = ((await api('GET', '/models')).json.data?.models ?? []).find((m) => m.model_id === modelID)
    check('备注确实改到了（这次编辑真的提交了）', afterEdit?.note === '备注改动', JSON.stringify(afterEdit?.note))
    check('只改备注不动已声明的输入能力（不许静默降级为仅文本）',
      JSON.stringify(afterEdit?.input_modalities) === JSON.stringify(['text', 'image']),
      JSON.stringify(afterEdit?.input_modalities))

    // ③‴ 新建时就能**声明**能力（用户要求：平台没法自动检测，只能让人填）。
    const modelID2 = `${modelID}-vision`
    await waitEditorGone(page, '模型编辑卡收起')
    await clickJS(page, groupAddExpr)
    await page.waitFor(`return !!(${cardExpr('model_id')})`, { label: '新建模型卡片展开', timeout: 8000 })
    await sleep(400)
    await selectByFieldID(page, 'vendor', provID)
    await page.type(modelCardField('model_id'), modelID2)
    if (!(await page.box(capWrapperExpr))) throw new Error('新建模型卡片里找不到「图片」输入能力勾选框')
    // 落到 wrapper **里面那个真实 `input`** 上（不是按坐标点 wrapper）：antd 把这颗 input 藏成
    // 透明 + 绝对定位，按坐标点在实测里出现过「点了、`checked` 仍是 false」（打在了 wrapper 的
    // 文字/padding 上），而这一下是后面两条断言的唯一入口。判据不变：下面读的仍是 DOM 里的
    // `checked` 与 REST 回带的 `input_modalities`。
    const tickedByJS = await clickJS(page, `${capWrapperExpr}?.querySelector('input')`)
    check('新建模型时「图片」勾选框点得到（落到真实 input）', tickedByJS === true, String(tickedByJS))
    let tickedNow = null
    for (let i = 0; i < 20 && tickedNow !== true; i++) {
      await sleep(100)
      tickedNow = await page.evaluate(`const w = ${capWrapperExpr}; return w ? !!w.querySelector('input')?.checked : null`)
    }
    check('新建模型时能勾上「图片」输入能力', tickedNow === true, JSON.stringify(tickedNow))
    // 截图取在**保存前**：卡片收起之后再截就只剩清单，看不出"表单里真的有能力这一项"。
    await page.shot('22c-settings-model-capability-form')
    await page.click(modelCardSave)
    await waitEditorGone(page, '新建卡收起（这条新建的写面已返回）')
    const created2 = ((await api('GET', '/models')).json.data?.models ?? []).find((m) => m.model_id === modelID2)
    check('新建时勾的「图片」落库为 input_modalities=["text","image"]',
      JSON.stringify(created2?.input_modalities) === JSON.stringify(['text', 'image']),
      JSON.stringify(created2?.input_modalities))

    // ④ 文案纪律（ADR-22）：面板里**不许**出现任何底座词汇，也不许渲染 sync[] 的机器面字段。
    //    词汇表按**在册 agent 的 id** 现算（见 runtimeWordRe），不写死具体底座名。
    const panelCopy = await page.evaluate(`return document.querySelector('.fg-model-panel').innerText`)
    const lower = panelCopy.toLowerCase()
    const banned = [...AGENTS.map((a) => String(a.id ?? '').toLowerCase()).filter(Boolean), 'runtime', '底座', 'provider']
    const hit = banned.filter((w) => lower.includes(w))
    check('面板正文零底座词汇（ADR-22）', hit.length === 0,
      hit.length ? `命中 ${JSON.stringify(hit)}；行=${JSON.stringify(panelCopy.split('\n').filter((l) => hit.some((w) => l.toLowerCase().includes(w))))}` : '干净')
    check('逐后端的机器面明细没有被渲染（sync[] 属诊断面）',
      !runtimeWordRe().test(panelCopy), panelCopy.slice(0, 160))

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
  // 连不上要说清"下一步做什么"：本脚本**刻意不自起服务**（它验的是"人正在看的那个实例"），
  // 所以"连不上"的第一原因是 `make dev` 没在跑，而不是页面坏了——裸 `TypeError: fetch failed`
  // 会让人去查前端。
  const health = await api('GET', '/health').catch((err) => {
    check('后端可达', false,
      `${MATE_URL} 连不上（${err?.cause?.code ?? err?.message ?? err}）——先在另一个终端起 \`make dev\``)
    return null
  })
  if (!health) {
    throw new Error(`靶不可达：${MATE_URL}。先起 \`make dev\`（本目标验的是那个正在跑的实例，脚本不自起服务）。`)
  }
  check('后端 health 可达', health.json.code === 0, `${MATE_URL} → ${JSON.stringify(health.json)}`)
  // 靶的正确性先于一切：靶不对，后面每条断言验的都不是交付件（P2 的启动断言）。
  await assertServingBuiltArtifact()
  // 场景按"在册事实"驱动（P2）：底座由构建期固定，写死某个 agent 会把脚本陈旧演成页面超时。
  await resolveRuntimeFacts()

  await seedSkills()
  check('验收用 skill 已就绪（M2 写面）', true, Object.values(SKILL_DEFS).map((d) => d.name).join(', '))

  const { child, port } = await launchChrome()
  const browser = await connectCDP(
    (await (await fetch(`http://127.0.0.1:${port}/json/version`)).json()).webSocketDebuggerUrl,
  )

  // `model`：真 Chrome 上跑绿过、并已进默认集合；2026-10-13 修掉它的时序竞态（见 `waitEditorGone`）。
  const scenarios = {
    cap: scenarioCapability,
    auto: scenarioAutomation,
    cmd: scenarioCommands,
    wsdir: scenarioWorkspaceDir,
    // `model` 同样进默认集合；要单独回归就 `pnpm ui:accept model`。
    model: scenarioSettingsModel,
  }
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

  // 收尾：当前 agent 切回**开跑时那个**（环境还原），并清理验收用 skill
  if (INITIAL_DEFAULT_AGENT) await setDefaultAgent(INITIAL_DEFAULT_AGENT)
  const after = await api('GET', '/agents')
  check(`默认 agent 已还原为 ${INITIAL_DEFAULT_AGENT}`,
    (after.json.data ?? []).find((a) => a.id === INITIAL_DEFAULT_AGENT)?.is_default === true,
    `agents=${(after.json.data ?? []).map((a) => `${a.id}:${a.is_default}`).join(' ')}`)
  await cleanupSkills()
  await cleanupCreated()

  if (!process.env.KEEP_CHROME) child.kill('SIGKILL')
  const failed = results.filter((r) => !r.ok)
  console.log(`\n——— 汇总：${results.length - failed.length}/${results.length} 通过` +
    (skips.length ? `，${skips.length} 条显式跳过` : '') + ` ———`)
  if (failed.length) {
    for (const f of failed) console.log(`  ❌ ${f.name} — ${f.detail}`)
  }
  if (skips.length) {
    // 跳过必须**显式可见**：既不假装绿，也不假装红（本仓隔离纪律对"不可判定"的同一条要求）。
    for (const s of skips) console.log(`  ⏭️  ${s.name} — 跳过：${s.why}`)
  }
  if (failed.length) process.exitCode = 1
}

main().catch((e) => {
  console.error('验收脚本自身失败:', e)
  process.exitCode = 2
})

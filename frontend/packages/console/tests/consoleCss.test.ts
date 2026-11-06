import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, it } from 'node:test'

// 面 C：执行步骤盒式卡（原型 §273 .step）的样式口径——直接读源文本做断言，
// 避免 import console.css.ts（它依赖 @ark-mate/ui 的 tokens，node ESM 解析不了该包的无扩展名入口）。
// 去掉 ${...} 模板插值：否则块尾的 '}' 会被 token 插值里的右花括号提前截断。
const raw = readFileSync(fileURLToPath(new URL('../src/console.css.ts', import.meta.url)), 'utf8')
const src = raw.replace(/\$\{[^}]*\}/g, 'TOKEN')

function block(selector: string): string {
  const start = src.indexOf(selector)
  assert.ok(start >= 0, `找不到 ${selector} 样式块`)
  return src.slice(start, src.indexOf('}', start))
}

describe('执行步骤盒式卡（.fg-tool）', () => {
  it('.fg-tool 是 hairline 边框 + 11px 圆角的卡片（不再是扁平行）', () => {
    const b = block('.fg-tool {')
    assert.match(b, /border:\s*1px solid/)
    assert.match(b, /border-radius:\s*11px/)
  })

  it('.fg-tool-head 头行有内边距（9px 13px，对齐 step-h）', () => {
    assert.match(block('.fg-tool-head {'), /padding:\s*9px 13px/)
  })

  it('.fg-process-list 卡之间有 8px 间距', () => {
    assert.match(block('.fg-process-list {'), /gap:\s*8px/)
  })
})

describe('Composer 忙态（.fg-running / .fg-waiting / 投递 pill）', () => {
  it('.fg-composer.fg-running 是品牌描边 + 呼吸动画', () => {
    const b = block('.fg-composer.fg-running {')
    assert.match(b, /border-color:/)
    assert.match(b, /animation:\s*fg-run-breathe/)
  })

  it('@keyframes fg-run-breathe 存在', () => {
    assert.ok(src.includes('@keyframes fg-run-breathe'))
  })

  it('.fg-composer.fg-waiting 是 warning 描边', () => {
    const b = block('.fg-composer.fg-waiting {')
    assert.match(b, /border-color:/)
  })

  it('.fg-delivery-pill 是 pill 形态（999 圆角）', () => {
    const b = block('.fg-delivery-pill {')
    assert.match(b, /border-radius:\s*TOKENpx/)
  })
})

describe('输入卡圆角收口（radiusComposer，防止被内联覆盖）', () => {
  // 两个输入卡实现：对话页 Composer、新建会话 StartPanel。它们外形靠 .fg-composer 类，
  // 圆角口径是 tokens.radiusComposer。历史上 Composer 内联过 borderRadius: radiusSm(7px)，
  // 把类上的圆角压掉了，导致输入卡比全站圆钮/pill 偏方——重开后无声回退过，因此钉死。
  const consoleSrc = readFileSync(
    fileURLToPath(new URL('../src/components/Composer.tsx', import.meta.url)),
    'utf8',
  )
  const startSrc = readFileSync(
    fileURLToPath(new URL('../src/components/StartPanel.tsx', import.meta.url)),
    'utf8',
  )

  it('.fg-composer 的圆角来自 tokens.radiusComposer（不写死像素）', () => {
    const b = block('.fg-composer {')
    assert.match(b, /border-radius:\s*TOKENpx/)
  })

  it('Composer 不再内联写死 borderRadius（交给 .fg-composer 类）', () => {
    // 只要出现 `borderRadius:` 就说明有内联圆角，会覆盖类上的 radiusComposer。
    assert.doesNotMatch(consoleSrc, /borderRadius\s*:/)
  })

  it('StartPanel 输入卡用 tokens.radiusComposer（不写死 14/16）', () => {
    assert.match(startSrc, /borderRadius:\s*tokens\.radiusComposer/)
    assert.doesNotMatch(startSrc, /borderRadius:\s*\d/)
  })
})

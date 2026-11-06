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

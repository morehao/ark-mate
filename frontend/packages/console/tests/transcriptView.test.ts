import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import type { StreamEntry } from '@ark-mate/types'

import {
  buildProcessBlocks,
  buildTurnRows,
  foldThoughts,
  formatDurationMS,
  mergeToolUpdate,
  summarizeToolGroup,
  toolDetail,
  toolView,
  type TurnModel,
} from '../src/console/transcriptView.ts'

const base = (kind: StreamEntry['kind'], id: string, extra: Partial<StreamEntry> = {}): StreamEntry => ({
  kind,
  id,
  ...extra,
})

describe('工具语义行', () => {
  it('read 从 ACP kind/入参得到「已读取 + 文件名 + 行号」', () => {
    const view = toolView(base('tool', 't1', {
      title: 'Read',
      toolKind: 'read',
      status: 'completed',
      input: { file_path: '/repo/src/transcriptView.ts', start_line: 10, end_line: 24 },
    }))
    assert.equal(view.group, 'read')
    assert.equal(view.icon, 'read')
    assert.equal(view.statusLabel, '已读取')
    assert.equal(view.primary, 'transcriptView.ts')
    assert.equal(view.secondary, 'L10–24')
    assert.equal(view.link, true)
  })

  it('command 成功不额外挂状态词，只在失败时给「执行失败」', () => {
    const ok = toolView(base('tool', 't2', {
      title: 'Bash',
      toolKind: 'execute',
      status: 'completed',
      input: { command: 'pnpm --filter console test' },
    }))
    assert.equal(ok.group, 'command')
    assert.equal(ok.statusLabel, '')
    assert.equal(ok.primary, 'pnpm --filter console test')

    const failed = toolView(base('tool', 't3', {
      title: 'Bash',
      toolKind: 'execute',
      status: 'failed',
      input: { command: 'pnpm test' },
    }))
    assert.equal(failed.statusLabel, '执行失败')
    assert.equal(failed.failed, true)
  })

  it('web_search 的 queries 数组取首条查询，失败态用「搜索失败」', () => {
    const view = toolView(base('tool', 'ws', {
      title: 'web_search',
      toolKind: 'other',
      status: 'failed',
      input: { queries: ['西安 明天 天气 预报', 'Xi an weather'] },
    }))
    assert.equal(view.group, 'research')
    assert.equal(view.primary, '西安 明天 天气 预报')
    assert.equal(view.statusLabel, '搜索失败')
  })

  it('连续工具摘要取主导类别 + 第一个对象', () => {
    const reads = [
      toolView(base('tool', 'r1', { title: 'Read', toolKind: 'read', status: 'completed', input: { file_path: '/a/one.ts' } })),
      toolView(base('tool', 'r2', { title: 'Read', toolKind: 'read', status: 'completed', input: { file_path: '/a/two.ts' } })),
      toolView(base('tool', 's1', { title: 'Grep', toolKind: 'search', status: 'completed', input: { pattern: 'fold' } })),
    ]
    assert.equal(summarizeToolGroup(reads, false).text, '查看文件、定位代码：one.ts')
    assert.equal(summarizeToolGroup(reads, true).text, '正在查看文件、定位代码：one.ts')
  })
})

describe('过程块', () => {
  it('连续 thought/tool 在允许位置收成摘要，早期过程逐条显示', () => {
    const thought = foldThoughts([base('thought', 'th', { text: '先看一下' })], false)[0]
    const tool = base('tool', 't1', { title: 'Read', toolKind: 'read', status: 'completed', input: { file_path: '/a/x.ts' } })
    const blocks = buildProcessBlocks([thought, tool], { running: false, foldFrom: 0 })
    assert.equal(blocks.length, 1)
    assert.equal(blocks[0].kind, 'tool-group')
    if (blocks[0].kind === 'tool-group') {
      assert.equal(blocks[0].summary.text, '查看 x.ts')
      assert.equal(blocks[0].items.length, 2)
    }

    const unfolded = buildProcessBlocks([thought, tool], { running: true, foldFrom: 1 })
    assert.deepEqual(unfolded.map((block) => block.kind), ['thought', 'tool'])
  })
})

describe('整轮折叠', () => {
  const entries = (): StreamEntry[] => [
    base('user', 'u', { text: '看下这个问题' }),
    base('assistant', 'a1', { text: '先把两处结构对齐：身份头、过程流、正文锚点，这些都和当前卡片式布局不同。' }),
    base('thought', 'th', { text: '先确认数据结构' }),
    base('tool', 't1', {
      title: 'Read',
      toolKind: 'read',
      status: 'completed',
      input: { file_path: '/a/AgentRenderer.tsx' },
    }),
    base('assistant', 'a2', { text: '结论。' }),
    base('system', 's', { tone: 'ok', seq: 9 }),
  ]

  it('最长正文 + 最后正文留作锚点，其余过程收进「已完成」', () => {
    const folded = foldThoughts(entries(), false)
    const rows = buildTurnRows(folded, {
      streaming: false,
      durationForSeq: (seq) => (seq === 9 ? 65000 : undefined),
      indexOfId: (id) => entries().findIndex((entry) => entry.id === id),
    })
    assert.equal(rows.length, 1)
    const turn = rows[0] as TurnModel
    assert.equal(turn.kind, 'turn')
    assert.equal(turn.anchors.length, 2)
    assert.equal(turn.completed?.title, '已完成 1m 5s')
    assert.equal(turn.completed?.between.length, 1)
    assert.equal(turn.completed?.between[0].blocks.length, 1)
    assert.equal(turn.completed?.after.length, 0)
    assert.equal(turn.live, false)
  })

  it('流式最后一轮不生成 completed，过程仍可见', () => {
    const folded = foldThoughts(entries().slice(0, 5), true)
    const rows = buildTurnRows(folded, { streaming: true, indexOfId: () => 0 })
    const turn = rows[0] as TurnModel
    assert.equal(turn.live, true)
    assert.equal(turn.completed, undefined)
    assert.ok(turn.blocks.length > 0)
  })

  it('服务端说在跑（runActive）但本端没发请求时，最后一轮仍按 live 渲染、不挂「已完成」', () => {
    // 复现：定时任务手动触发/投递是**服务端发起**的运行，本端 `streaming` 恒为 false，
    // 头栏（读 run.status）说「正在执行 Bash」，正文却因只看 streaming 把这一轮当已结束。
    // 这一轮还没收到终态（无 system tone:ok），只有一个正在跑的工具。
    const runningTurn = (): StreamEntry[] => [
      base('user', 'u', { text: '用天气查询技能查一下北京今天和未来几天的天气……' }),
      base('tool', 't', { title: 'Bash', toolKind: 'execute', status: 'running', input: { command: 'weather-query' } }),
    ]
    const folded = foldThoughts(runningTurn(), false)
    const rows = buildTurnRows(folded, {
      streaming: false,
      runActive: true,
      indexOfId: (id) => runningTurn().findIndex((entry) => entry.id === id),
    })
    assert.equal(rows.length, 1)
    const turn = rows[0] as TurnModel
    assert.equal(turn.live, true)
    // 关键：不再挂「已完成」灰框，也不把过程折进 completed。
    assert.equal(turn.completed, undefined)
    assert.ok(turn.blocks.length > 0)
  })

  it('runActive 但最后一轮已收到终态（endSeq 已定）时，不把已完成的轮倒灌成未完成', () => {
    // 会话整体还在忙（可能是下一次运行），但这一轮已经拿到 system tone:ok 的终态：它是真的完成了。
    const folded = foldThoughts(entries(), false) // 末尾自带 system tone:ok（seq 9）
    const rows = buildTurnRows(folded, {
      streaming: false,
      runActive: true,
      durationForSeq: (seq) => (seq === 9 ? 65000 : undefined),
      indexOfId: (id) => entries().findIndex((entry) => entry.id === id),
    })
    const turn = rows[0] as TurnModel
    assert.equal(turn.live, false)
    assert.equal(turn.completed?.title, '已完成 1m 5s')
  })

  it('多段正文只保留「最长 + 最后」，中间段进入过程', () => {
    const folded = foldThoughts([
      base('user', 'u4', { text: '多段' }),
      base('assistant', 'b1', { text: '短。' }),
      base('assistant', 'b2', { text: '这是一段很长的中间正文，它会被选成锚点。' }),
      base('tool', 't5', { title: 'Read', toolKind: 'read', status: 'completed', input: { file_path: '/a/mid.ts' } }),
      base('assistant', 'b3', { text: '最后一段。' }),
      base('system', 's4', { tone: 'ok', seq: 30 }),
    ], false)
    const rows = buildTurnRows(folded, { streaming: false, durationForSeq: () => 2000, indexOfId: () => 0 })
    const turn = rows[0] as TurnModel
    assert.deepEqual(turn.anchors.map((item) => item.id), ['b2', 'b3'])
    assert.equal(turn.completed?.between.length, 1)
  })

  it('锚点选择：最长正文与最后正文重合时不重复，顺序按原文', () => {
    const folded = foldThoughts([
      base('user', 'u5', { text: '一个回答就够' }),
      base('assistant', 'b4', { text: '只有这一段正文，它同时是最长与最后。' }),
      base('system', 's5', { tone: 'ok', seq: 40 }),
    ], false)
    const rows = buildTurnRows(folded, {
      streaming: false,
      durationForSeq: () => 3000,
      indexOfId: () => 0,
    })
    const turn = rows[0] as TurnModel
    assert.deepEqual(turn.anchors.map((anchor) => anchor.id), ['b4'])
  })

  it('只有正文、没有过程时也保留锚点（不生成空折叠）', () => {
    const folded = foldThoughts([
      base('user', 'u3', { text: '直接回答' }),
      base('assistant', 'a4', { text: '答案。' }),
      base('system', 's3', { tone: 'ok', seq: 20 }),
    ], false)
    const rows = buildTurnRows(folded, {
      streaming: false,
      durationForSeq: () => 1200,
      indexOfId: () => 0,
    })
    const turn = rows[0] as TurnModel
    assert.equal(turn.completed, undefined)
    assert.equal(turn.anchors.length, 1)
    assert.equal(turn.blocks.length, 0)
    assert.equal(turn.completion, '已完成 1s')
  })

  it('轮末 system err 不折进过程流，轮标记为中断', () => {
    const folded = foldThoughts([
      base('user', 'u2', { text: '跑一下' }),
      base('assistant', 'a3', { text: '开始执行。' }),
      base('system', 'e', { tone: 'err', text: '失败' }),
    ], false)
    const rows = buildTurnRows(folded, { streaming: false, indexOfId: () => 0 })
    assert.equal(rows.length, 2)
    assert.equal(rows[0].kind, 'turn')
    assert.equal((rows[0] as TurnModel).failed, true)
    assert.equal(rows[1].kind, 'entry')
  })

  it('权限卡不切轮：user → tool → permission → tool → assistant 只有一个轮/身份头', () => {
    const entries = (): StreamEntry[] => [
      base('user', 'u', { text: '看一下' }),
      base('tool', 't1', { title: 'Read', toolKind: 'read', status: 'completed', input: { file_path: '/a.ts' } }),
      base('permission', 'p', { resolved: true, title: '允许读取', decision: '允许 · 本次' }),
      base('tool', 't2', { title: 'Bash', toolKind: 'execute', status: 'completed', input: { command: 'ls' } }),
      base('assistant', 'a', { text: '结果如下。' }),
      base('system', 's', { tone: 'ok', seq: 9 }),
    ]
    const rows = buildTurnRows(foldThoughts(entries(), false), {
      streaming: false,
      durationForSeq: () => 1000,
      indexOfId: (id) => entries().findIndex((entry) => entry.id === id),
    })
    assert.equal(rows.length, 1)
    const turn = rows[0] as TurnModel
    assert.equal(turn.user?.id, 'u')
    assert.ok(turn.entryIds.includes('p'))
    assert.equal(turn.anchors.length, 1)
    // 已结算的卡按原顺序留在过程流里（tool1 → 卡 → tool2），不是另起一行的 entry。
    assert.deepEqual(turn.completed?.before.map((block) => block.kind), ['tool', 'notice', 'tool'])
  })

  it('未结算的权限卡不切轮、也不进条目流（它只在确认区）', () => {
    const entries = (): StreamEntry[] => [
      base('user', 'u', { text: '看一下' }),
      base('tool', 't1', { title: 'Read', toolKind: 'read', status: 'completed', input: { file_path: '/a.ts' } }),
      base('permission', 'p', { resolved: false, title: '允许读取' }),
      base('tool', 't2', { title: 'Bash', toolKind: 'execute', status: 'completed', input: { command: 'ls' } }),
      base('assistant', 'a', { text: '结果如下。' }),
      base('system', 's', { tone: 'ok', seq: 9 }),
    ]
    const rows = buildTurnRows(foldThoughts(entries(), false), {
      streaming: false,
      durationForSeq: () => 1000,
      indexOfId: (id) => entries().findIndex((entry) => entry.id === id),
    })
    assert.equal(rows.length, 1)
    const turn = rows[0] as TurnModel
    assert.ok(!turn.entryIds.includes('p'))
    // 两张卡之间的工具仍然相邻 → 收成摘要；关键是没有第二个 Agent 头。
    assert.deepEqual(turn.completed?.before.map((block) => block.kind), ['tool-group'])
  })
})

describe('工具详情渲染器', () => {
  it('read：路径 + 代码预览 + 行号（起始行只认调用参数）', () => {
    const detail = toolDetail(base('tool', 'd1', {
      title: 'Read',
      toolKind: 'read',
      status: 'completed',
      input: { file_path: '/repo/src/a.ts', start_line: 10 },
      output: 'const a = 1\nconst b = 2',
    }))
    assert.equal(detail?.kind, 'read')
    assert.equal(detail?.path, '/repo/src/a.ts')
    assert.deepEqual(detail?.lines, [
      { no: 10, text: 'const a = 1' },
      { no: 11, text: 'const b = 2' },
    ])
  })

  it('read：没有起始行时不编号；没有结果时如实说不可用', () => {
    const noStart = toolDetail(base('tool', 'd2', {
      title: 'Read',
      toolKind: 'read',
      status: 'completed',
      input: { file_path: '/a.ts' },
      output: 'x',
    }))
    assert.deepEqual(noStart?.lines, [{ text: 'x' }])

    const noOutput = toolDetail(base('tool', 'd3', {
      title: 'Read',
      toolKind: 'read',
      status: 'completed',
      input: { file_path: '/a.ts' },
    }))
    assert.equal(noOutput?.lines, undefined)
    assert.ok(noOutput?.notes.some((note) => note.includes('不可用')))
  })

  it('edit：从调用参数的 old/new 生成 diff 行；没有 diff 数据时给结果摘要', () => {
    const detail = toolDetail(base('tool', 'e1', {
      title: 'Edit',
      toolKind: 'edit',
      status: 'completed',
      input: { file_path: '/a.ts', old_string: 'old', new_string: 'new' },
    }))
    assert.equal(detail?.kind, 'diff')
    assert.equal(detail?.diffSource, 'input')
    assert.deepEqual(detail?.diff?.map((line) => line.kind), ['meta', 'del', 'add'])

    const summary = toolDetail(base('tool', 'e2', {
      title: 'Edit',
      toolKind: 'edit',
      status: 'completed',
      input: { file_path: '/a.ts' },
      output: 'Applied 1 edit',
    }))
    assert.equal(summary?.kind, 'summary')
    assert.equal(summary?.summary, 'Applied 1 edit')
  })

  it('edit：结果里的 unified diff 优先按 diff 渲染', () => {
    const detail = toolDetail(base('tool', 'e3', {
      title: 'Edit',
      toolKind: 'edit',
      status: 'completed',
      input: { file_path: '/a.ts' },
      output: '--- a/a.ts\n+++ b/a.ts\n@@ -1,1 +1,1 @@\n-old\n+new',
    }))
    assert.equal(detail?.kind, 'diff')
    assert.equal(detail?.diffSource, 'output')
    assert.deepEqual(detail?.diff?.map((line) => line.kind), ['meta', 'meta', 'meta', 'del', 'add'])
  })

  it('execute：命令行 + stderr + exit code（分节标记 / 结构化字段）', () => {
    const parsed = toolDetail(base('tool', 'x1', {
      title: 'Bash',
      toolKind: 'execute',
      status: 'completed',
      input: { command: 'ls /nope' },
      output: '[stderr]\nls: /nope: No such file or directory\n[exit code: 1]',
    }))
    assert.equal(parsed?.kind, 'command')
    assert.equal(parsed?.command, 'ls /nope')
    assert.equal(parsed?.stderr, 'ls: /nope: No such file or directory')
    assert.equal(parsed?.exitCode, 1)

    const structured = toolDetail(base('tool', 'x2', {
      title: 'Bash',
      toolKind: 'execute',
      status: 'completed',
      input: { command: 'echo ok' },
      output: { stdout: 'ok', exit_code: 0 },
    }))
    assert.equal(structured?.stdout, 'ok')
    assert.equal(structured?.exitCode, 0)

    // 没有分节标记时不冒充 stdout：整段只标「输出」。
    const plain = toolDetail(base('tool', 'x3', {
      title: 'Bash',
      toolKind: 'execute',
      status: 'completed',
      input: { command: 'cat /nope' },
      output: 'cat: /nope: No such file or directory',
    }))
    assert.equal(plain?.output, 'cat: /nope: No such file or directory')
    assert.equal(plain?.stdout, undefined)
    assert.equal(plain?.stderr, undefined)
  })

  it('search：查询词 + 结果列表；没有结果时只留查询与不可用说明', () => {
    const detail = toolDetail(base('tool', 's1', {
      title: 'web_search',
      toolKind: 'other',
      status: 'completed',
      input: { queries: ['西安 明天 天气'] },
      output: '结果一\n结果二',
    }))
    assert.equal(detail?.kind, 'search')
    assert.equal(detail?.query, '西安 明天 天气')
    assert.deepEqual(detail?.results, ['结果一', '结果二'])

    const empty = toolDetail(base('tool', 's2', {
      title: 'web_search',
      toolKind: 'other',
      status: 'completed',
      input: { queries: ['没有结果'] },
    }))
    assert.equal(empty?.results?.length, 0)
    assert.ok(empty?.notes.some((note) => note.includes('不可用')))
  })

  it('数据不足时不猜：没有路径的 read 退回纯文本（detail 为 null）', () => {
    const detail = toolDetail(base('tool', 'n1', {
      title: 'Read',
      toolKind: 'read',
      status: 'completed',
      input: { hint: 'somewhere' },
      output: 'text',
    }))
    assert.equal(detail, null)
  })
})

describe('工具增量合并', () => {
  it('空 title 不覆盖 tool_call 的标题，缺省的 input/output 保留旧值', () => {
    const entry = base('tool', 't9', {
      toolId: 'c1',
      title: 'Bash',
      status: 'in_progress',
      toolKind: 'execute',
      input: { command: 'ls' },
    })
    const merged = mergeToolUpdate(entry, {
      title: '',
      status: 'completed',
      input: undefined,
      output: { stdout: 'ok' },
      failReason: 'sandbox_denied',
      bizCode: 'sandbox_denied',
    })
    assert.equal(merged.title, 'Bash')
    assert.equal(merged.status, 'completed')
    assert.deepEqual(merged.input, { command: 'ls' })
    assert.deepEqual(merged.output, { stdout: 'ok' })
    assert.equal(merged.failReason, 'sandbox_denied')
    assert.equal(merged.bizCode, 'sandbox_denied')
  })
})

describe('时长格式', () => {
  it('65s → 1m 5s；不足 1s 不编数字', () => {
    assert.equal(formatDurationMS(65000), '1m 5s')
    assert.equal(formatDurationMS(500), '<1s')
    assert.equal(formatDurationMS(0), '')
    assert.equal(formatDurationMS(3660000), '1h 1m')
  })
})

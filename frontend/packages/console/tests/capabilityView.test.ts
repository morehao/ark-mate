import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import {
  FOLLOW_DEFAULT,
  isEmptyPatch,
  mcpConnState,
  mcpCreateBody,
  mcpFormPatch,
  mcpFormValues,
  mcpRowDescription,
  mcpTransportView,
  parseMcpJson,
  pinnedCapabilitiesView,
  selectionPayload,
  selectionPillText,
  selectionIsDefault,
  skippedWarningText,
  skillCreateBody,
  skillFormPatch,
  skillFormValues,
  skillStatusView,
  splitArgs,
  transportLabel,
  transportLongLabel,
  type McpFormValues,
  type SkillFormValues,
} from '../src/console/capabilityView.ts'

// 这一组钉的是**写面语义**，不是像素：只提交变化键、旧 http 不被静默改型、
// env/header 编辑态不回写（值不可读）、runtimes 空 = 全部。这些分支出错时界面照样工作，
// 只有定义在磁盘上悄悄换了样子——所以用 Node 单测逐条钉。

const baseSkill = (): SkillFormValues => ({
  id: 'weekly-report',
  name: '周报生成器',
  description: '从会话里提炼',
  runtimes: [],
  scope: 'global',
  body: '# 周报',
})

const baseMcp = (): McpFormValues => ({
  id: 'filesystem',
  name: 'filesystem',
  transport: 'stdio',
  originalTransport: 'stdio',
  command: '/usr/local/bin/npx',
  args: '-y @modelcontextprotocol/server-filesystem',
  url: '',
  env: [],
  headers: [],
})

describe('transport 三值映射', () => {
  it('旧 http 与 streamable_http 都显示为 Streamable HTTP', () => {
    assert.equal(mcpTransportView('http'), 'streamable_http')
    assert.equal(mcpTransportView('streamable_http'), 'streamable_http')
    assert.equal(transportLabel('http'), 'HTTP')
    assert.equal(transportLongLabel('http'), 'Streamable HTTP')
    assert.equal(transportLabel('stdio'), 'STDIO')
    assert.equal(transportLabel('sse'), 'SSE')
  })
})

describe('MCP 状态派生', () => {
  const row = {
    defEnabled: true,
    lastProbeError: undefined as string | undefined,
    lastProbeAt: undefined as string | undefined,
    unresolvedSecretKeys: [] as string[],
  }

  it('优先级：停用 > 失败 > 待配置 > 未探测 > 已连接', () => {
    assert.equal(mcpConnState({ ...row, defEnabled: false, lastProbeError: 'x' }).key, 'disabled')
    assert.equal(mcpConnState({ ...row, lastProbeError: 'x' }).key, 'error')
    assert.equal(mcpConnState({ ...row, unresolvedSecretKeys: ['A'] }).key, 'needs_setup')
    assert.equal(mcpConnState(row).key, 'pending')
    assert.equal(mcpConnState({ ...row, lastProbeAt: '2026-10-17T00:00:00Z' }).key, 'connected')
  })

  it('描述行是事实而不是复述标签', () => {
    const error = mcpConnState({ ...row, lastProbeError: 'MCP error -32000: Connection closed' })
    assert.match(mcpRowDescription({ toolCount: 0, unresolvedSecretKeys: [] }, error, 'MCP error -32000: Connection closed'), /Connection closed/)
    const setup = mcpConnState({ ...row, defEnabled: true, unresolvedSecretKeys: ['PG_TOKEN'] })
    assert.match(mcpRowDescription({ toolCount: 0, unresolvedSecretKeys: ['PG_TOKEN'] }, setup), /PG_TOKEN/)
  })
})

describe('skill 状态点', () => {
  it('禁用 / 告警 / 启用 三态', () => {
    assert.equal(skillStatusView({ enabled: false }).label, '已停用')
    assert.equal(skillStatusView({ enabled: true, warnings: [{ severity: 'warning', pattern: 'p', message: 'm' }] }).label, '需处理')
    assert.equal(skillStatusView({ enabled: true, warnings: [] }).label, '已启用')
  })
})

describe('skill 写面：只提交变化键', () => {
  it('不改 → 空补丁（不发请求、不碰 mtime）', () => {
    const v = baseSkill()
    assert.deepEqual(skillFormPatch(v, { ...v, runtimes: [] }), {})
    assert.equal(isEmptyPatch(skillFormPatch(v, { ...v })), true)
  })

  it('改名只带 name', () => {
    const v = baseSkill()
    assert.deepEqual(skillFormPatch(v, { ...v, name: '新名字' }), { name: '新名字' })
  })

  it('清空 runtimes 是显式 []（不是不提交）', () => {
    const v = { ...baseSkill(), runtimes: ['dsh'] }
    assert.deepEqual(skillFormPatch(v, { ...v, runtimes: [] }), { runtimes: [] })
  })

  it('runtimes 是集合语义：顺序变不算改动', () => {
    const v = { ...baseSkill(), runtimes: ['dsh', 'other'] }
    assert.deepEqual(skillFormPatch(v, { ...v, runtimes: ['other', 'dsh'] }), {})
  })

  it('清空描述提交空串（清空），不是省略', () => {
    const v = baseSkill()
    assert.deepEqual(skillFormPatch(v, { ...v, description: '' }), { description: '' })
  })

  it('新建 body 的 runtimes 空数组就是「全部」，不写 ["*"]', () => {
    const body = skillCreateBody({ ...baseSkill(), id: '  weekly-report  ' })
    assert.equal(body.id, 'weekly-report')
    assert.deepEqual(body.runtimes, [])
    assert.equal(JSON.stringify(body).includes('*'), false)
  })

  it('读模型 → 表单值把缺省 scope 读成 global', () => {
    assert.equal(skillFormValues({ id: 'a', name: 'A' }).scope, 'global')
    assert.equal(skillFormValues({ id: 'a', name: 'A', scope: 'workspace' }).scope, 'workspace')
  })
})

describe('MCP 写面：不静默改型', () => {
  it('不改 → 空补丁', () => {
    const v = baseMcp()
    assert.deepEqual(mcpFormPatch(v, { ...v }), {})
  })

  it('旧 http 未动传输 → 补丁里没有 transport（原值保留）', () => {
    const v = { ...baseMcp(), transport: 'streamable_http' as const, originalTransport: 'http', command: '', url: 'https://mcp.example/v1' }
    assert.deepEqual(mcpFormPatch(v, { ...v, name: 'renamed' }), { name: 'renamed' })
  })

  it('旧 http 切到 SSE → 提交 sse', () => {
    const v = { ...baseMcp(), transport: 'streamable_http' as const, originalTransport: 'http', command: '', url: 'https://mcp.example/v1' }
    assert.deepEqual(mcpFormPatch(v, { ...v, transport: 'sse' }), { transport: 'sse', url: 'https://mcp.example/v1', command: null, args: null })
  })

  it('stdio → 远程：清掉 command/args（显式 null）', () => {
    const v = baseMcp()
    const next = { ...v, transport: 'sse' as const, url: 'https://mcp.example/sse' }
    assert.deepEqual(mcpFormPatch(v, next), {
      transport: 'sse',
      url: 'https://mcp.example/sse',
      command: null,
      args: null,
    })
  })

  it('远程 → stdio：清掉 url', () => {
    const v = { ...baseMcp(), transport: 'sse' as const, originalTransport: 'sse', command: '', args: '', url: 'https://mcp.example/sse' }
    const next = { ...v, transport: 'stdio' as const, command: '/usr/bin/node', args: 'server.js' }
    assert.deepEqual(mcpFormPatch(v, next), {
      transport: 'stdio',
      command: '/usr/bin/node',
      args: ['server.js'],
      url: null,
    })
  })

  it('参数是有序语义：顺序变了要提交', () => {
    const v = { ...baseMcp(), args: '--a --b' }
    assert.deepEqual(mcpFormPatch(v, { ...v, args: '--b --a' }), { args: ['--b', '--a'] })
  })

  it('编辑态永不提交 env/header（值不可读，顶层键整体替换会删引用）', () => {
    const v = mcpFormValues({
      id: 'srv',
      transport: 'stdio',
      env_keys: ['PLAIN'],
      secret_env_keys: ['TOKEN'],
      secret_header_keys: ['Authorization'],
      tool_count: 0,
    })
    const next = { ...v, env: [...v.env, { key: 'NEW', value: 'store:x/y', secret: true, existed: false, removed: false }] }
    assert.deepEqual(mcpFormPatch(v, next), {})
  })

  it('读模型 → 表单值只有键名，secret 标记正确', () => {
    const v = mcpFormValues({
      id: 'srv',
      transport: 'stdio',
      env_keys: ['PLAIN'],
      secret_env_keys: ['TOKEN'],
      secret_header_keys: ['Authorization'],
      tool_count: 0,
    })
    assert.deepEqual(
      v.env.map((r) => [r.key, r.secret, r.existed, r.value]),
      [
        ['PLAIN', false, true, ''],
        ['TOKEN', true, true, ''],
      ],
    )
    assert.deepEqual(v.headers.map((r) => [r.key, r.secret]), [['Authorization', true]])
  })
})

describe('参数串解析', () => {
  it('按空白切分，引号包住含空格的参数', () => {
    assert.deepEqual(splitArgs('  -y   @scope/pkg  '), ['-y', '@scope/pkg'])
    assert.deepEqual(splitArgs('--path "/tmp/a b" --x'), ['--path', '/tmp/a b', '--x'])
    assert.deepEqual(splitArgs("'a b' c"), ['a b', 'c'])
  })
})

describe('MCP 新建 body', () => {
  it('secret 行进 secret_env，空白行不提交', () => {
    const v = mcpFormValues({ id: 'srv', transport: 'stdio', command: '/usr/bin/node', args: [], tool_count: 0 })
    const body = mcpCreateBody({
      ...v,
      env: [
        { key: 'PLAIN', value: 'v', secret: false, existed: false, removed: false },
        { key: 'TOKEN', value: 'store:team/token', secret: true, existed: false, removed: false },
        { key: 'BLANK', value: '', secret: true, existed: false, removed: false },
      ],
    })
    assert.deepEqual(body.env, { PLAIN: 'v' })
    assert.deepEqual(body.secret_env, { TOKEN: 'store:team/token' })
    assert.deepEqual(body.args, [])
  })
})

describe('MCP JSON 导入', () => {
  it('推断传输并拆分 env 引用', () => {
    const r = parseMcpJson(
      JSON.stringify({
        mcpServers: {
          github: {
            command: '/usr/local/bin/npx',
            args: ['-y', 'server-github'],
            env: { GITHUB_TOKEN: 'store:github/token', PLAIN: 'x' },
          },
          linear: { url: 'https://mcp.linear.app/sse' },
          remote: { url: 'https://mcp.example/v1' },
        },
      }),
    )
    assert.deepEqual(r.errors, [])
    assert.deepEqual(r.servers.map((s) => s.id), ['github', 'linear', 'remote'])
    assert.equal(r.servers[0].body.transport, 'stdio')
    assert.deepEqual(r.servers[0].body.secret_env, { GITHUB_TOKEN: 'store:github/token' })
    assert.deepEqual(r.servers[0].body.env, { PLAIN: 'x' })
    assert.equal(r.servers[1].body.transport, 'sse')
    assert.equal(r.servers[2].body.transport, 'streamable_http')
  })

  it('也接受省略 mcpServers 外层的片段', () => {
    const r = parseMcpJson(JSON.stringify({ fs: { command: '/bin/node' } }))
    assert.deepEqual(r.servers.map((s) => s.id), ['fs'])
  })

  it('非法 JSON / 空片段 / 缺 command 都给出可读错误', () => {
    assert.match(parseMcpJson('{').errors[0], /不是合法 JSON/)
    assert.match(parseMcpJson('{"mcpServers":{}}').errors[0], /没有可导入/)
    assert.match(parseMcpJson('{"mcpServers":{"a":{"transport":"stdio"}}}').errors[0], /必须给 command/)
    assert.match(parseMcpJson('{"mcpServers":{"a":{"transport":"sse"}}}').errors[0], /必须给 url/)
    assert.match(parseMcpJson('{"mcpServers":{"a":{}}}').errors[0], /必须给 command（stdio）或 url/)
  })
})

// ——— 会话能力选定的三态（含专家；A1/A3 的前端侧判据） ———
describe('capability selection wire shape', () => {
  it('三个面都未声明时不带 capabilities 键（缺省路径逐字节不变）', () => {
    assert.equal(selectionPayload(FOLLOW_DEFAULT), undefined)
    assert.equal(selectionIsDefault(FOLLOW_DEFAULT), true)
  })

  it('专家三态：null 不带键 / 空串明确不用 / 非空是 id', () => {
    assert.equal(selectionPayload({ expert: null, skills: null, mcps: null }), undefined)
    assert.deepEqual(selectionPayload({ expert: '', skills: null, mcps: null }), { expert: '' })
    assert.deepEqual(selectionPayload({ expert: 'data-analyst', skills: null, mcps: null }), {
      expert: 'data-analyst',
    })
  })

  it('显式空数组是「一个都不要」，与缺键相反', () => {
    assert.deepEqual(selectionPayload({ expert: null, skills: [], mcps: null }), { skills: [] })
    assert.equal(selectionIsDefault({ expert: null, skills: [], mcps: null }), false)
  })

  it('药丸文案反映专家与手选数量', () => {
    assert.equal(selectionPillText(FOLLOW_DEFAULT), '能力 · 跟随默认')
    assert.match(selectionPillText({ expert: 'data-analyst', skills: null, mcps: null }, '数据分析师'), /数据分析师/)
    assert.match(selectionPillText({ expert: null, skills: ['a', 'b'], mcps: ['m'] }), /技能 2/)
  })

  it('钉住快照回显含专家（含已删除标记）', () => {
    const v = pinnedCapabilitiesView({ expert: 'data-analyst', skills: ['a'], mcps: [] })
    assert.ok(v && v.text.includes('data-analyst') && v.text.includes('技能：a'))
    const del = pinnedCapabilitiesView({ expert: 'gone', expert_missing: true, skills: [] })
    assert.ok(del && del.text.includes('已删除'))
  })

  it('专家推荐被跳过的告警文案（无告警 → null）', () => {
    assert.equal(skippedWarningText(undefined), null)
    assert.equal(skippedWarningText([]), null)
    const text = skippedWarningText([{ kind: 'mcp', id: 'web', reason: '不承载 http 传输' }])
    assert.ok(text && text.includes('1 项') && text.includes('web') && text.includes('不承载 http 传输'))
  })
})

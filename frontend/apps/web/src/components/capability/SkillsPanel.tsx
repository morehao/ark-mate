import { Alert, Button, Modal, Space, Spin, Switch, Tag, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { PageContainer, tokens } from '@ark-mate/ui'
import {
  materializationMeta,
  materializationNeedsAttention,
  materializationSummary,
  skillWarningsView,
} from '../../console/capabilityView'
import { mono, smallTag, SectionHeader, SectionEmpty, Row, Card, SkillRuntimeTags, CapabilityEditorModal } from './shared'
import { useCapabilityData } from './useCapabilityData'

/** skill 定义：正文、投放对象（runtimes / scope）、启用与物化体检——定义来自 ~/.ark-mate/capabilities/skills。 */
export function SkillsPanel() {
  const {
    skills,
    edit,
    setEdit,
    editBusy,
    editErr,
    busy,
    toggleBusy,
    matTarget,
    setMatTarget,
    mat,
    matBusy,
    matErr,
    loadAll,
    refresh,
    openCreate,
    openEditSkill,
    submitEdit,
    doToggleSkill,
    openMaterialization,
    materializationButton,
    writeButtons,
  } = useCapabilityData('skills')

  return (
    <PageContainer
      title="技能"
      description="skill 定义：正文、投放对象（runtimes / scope）、启用与物化体检——定义来自 ~/.ark-mate/capabilities/skills"
      extra={
        <Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={() => void loadAll()}>
            刷新
          </Button>
        </Space>
      }
    >
      <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 16 }}>
        {/* ——— 全局：Skills ——— */}
        <Card>
          <SectionHeader
            title="Skills"
            hint="目录条目（平台唯一事实源，`~/.ark-mate/capabilities/skills/*.json`）：启用即按其 runtimes 投放（落点与格式由底座投影声明决定）、禁用即撤回副本（定义与正文保留）；「体检」看物化摘要是否被改动，并如实标注未实测/未配置的投影。新建/编辑走同一条闸门：正文扫描不过 → 40001 且**零字节落盘**"
            count={skills.length}
            loading={busy === 'skills'}
            onRefresh={() => refresh('skills')}
            extra={
              <Button size="small" onClick={() => openCreate('skill')}>
                新建 skill
              </Button>
            }
          />
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            {skills.length === 0 ? (
              <SectionEmpty text="暂无 skill（~/.ark-mate/capabilities/skills/*.json）" />
            ) : (
              skills.map((s) => {
                const warn = skillWarningsView(s.warnings)
                const enabled = s.enabled ?? true
                return (
                  <Row key={s.id}>
                    <b style={{ fontSize: 13, fontWeight: 600, minWidth: 110 }}>{s.name}</b>
                    <Tag style={{ marginInlineEnd: 0, fontSize: 11 }}>{s.id}</Tag>
                    {/* §13.2 S1：装载期正文扫描的非 critical 告警（critical 已拒载，不会出现在这里） */}
                    {warn && (
                      <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{warn.hint}</span>}>
                        <Tag color={warn.color} style={smallTag}>
                          {warn.text}
                        </Tag>
                      </Tooltip>
                    )}
                    <span style={{ flex: 1, fontSize: 12, color: tokens.textSecondary }}>{s.description}</span>
                    <SkillRuntimeTags runtimes={s.runtimes} scope={s.scope} />
                    {writeButtons('skill', s.id, s.name || s.id, () => openEditSkill(s))}
                    {materializationButton(s)}
                    <Tooltip title={enabled ? '禁用：撤回已投放副本（定义与正文保留，可恢复）' : '启用：按其 runtimes 重新投放'}>
                      <Switch
                        size="small"
                        checked={enabled}
                        loading={toggleBusy === s.id}
                        onChange={(next) => void doToggleSkill(s, next)}
                      />
                    </Tooltip>
                  </Row>
                )
              })
            )}
          </div>
        </Card>
        {/* ——— §13.4 S2：skill 物化体检（只读；修复走启停/重校，不在这里动手） ——— */}
        <Modal
          title={matTarget ? `物化体检 · ${matTarget.name}` : '物化体检'}
          open={matTarget !== null}
          onCancel={() => setMatTarget(null)}
          footer={null}
          width={780}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <span style={{ flex: 1, fontSize: 12.5, color: tokens.textSecondary }}>
              {mat ? materializationSummary(mat) : matBusy ? '正在体检…' : ''}
            </span>
            {mat && (
              <Tag color={mat.enabled ? 'green' : 'default'} style={smallTag}>
                {mat.enabled ? '已启用' : '已禁用'}
              </Tag>
            )}
            <Button
              size="small"
              icon={<ReloadOutlined />}
              loading={matBusy}
              disabled={!matTarget}
              onClick={() => matTarget && void openMaterialization(matTarget.id, matTarget.name)}
            >
              再体检
            </Button>
          </div>
          {matErr ? (
            <Alert type="error" showIcon message="体检失败" description={matErr} />
          ) : !mat ? (
            <div style={{ padding: 24, textAlign: 'center' }}>
              <Spin />
            </div>
          ) : (
            <>
              <Alert
                type="info"
                showIcon
                style={{ fontSize: 12, marginBottom: 10 }}
                message="体检只报不修"
                description="修复不走这里：启用/禁用 skill 会立刻重校（按平台版本覆盖回正确内容）；用户自有目录（无归属标记）平台不覆盖，需要手工处理。"
              />
              {mat.states.length === 0 ? (
                <SectionEmpty text={materializationSummary(mat)} />
              ) : (
                mat.states.map((st, i) => {
                  const meta = materializationMeta(st.status)
                  const attention = materializationNeedsAttention(st)
                  return (
                    <div
                      key={`${st.root}-${st.runtime ?? ''}-${i}`}
                      style={{
                        border: `1px solid ${attention ? tokens.borderStrong : tokens.border}`,
                        borderRadius: 8,
                        padding: '10px 12px',
                        marginBottom: 8,
                      }}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                        <Tooltip title={meta.hint}>
                          <Tag color={meta.color} style={smallTag}>
                            {meta.label}
                          </Tag>
                        </Tooltip>
                        <Tag style={smallTag}>{st.runtime || '单根'}</Tag>
                        <span style={{ ...mono, flex: 1, fontSize: 11.5 }}>{st.dir}</span>
                      </div>
                      <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 6 }}>{meta.hint}</div>
                      {st.hash && (
                        <div style={{ ...mono, fontSize: 11, color: tokens.textPlaceholder, marginTop: 4 }}>
                          期望 {st.hash.slice(0, 16)}…
                          {attention && st.actual_hash ? ` / 实测 ${st.actual_hash.slice(0, 16)}…` : ''}
                        </div>
                      )}
                      <div style={{ fontSize: 11, color: tokens.textPlaceholder, marginTop: 4 }}>
                        {st.owners && st.owners.length > 0 ? `归属：${st.owners.join('、')}` : '归属：未登记'}
                        {st.written_at ? ` · 写入 ${st.written_at}` : ''}
                      </div>
                      {st.detail && (
                        <div style={{ fontSize: 11.5, color: tokens.textSecondary, marginTop: 4 }}>{st.detail}</div>
                      )}
                    </div>
                  )
                })
              )}
              {mat.notes && mat.notes.length > 0 && (
                <Alert type="warning" showIcon style={{ fontSize: 12, marginTop: 8 }} message={mat.notes.join('；')} />
              )}
            </>
          )}
        </Modal>
      <CapabilityEditorModal
        edit={edit}
        setEdit={setEdit}
        submitEdit={submitEdit}
        editBusy={editBusy}
        editErr={editErr}
      />
      </div>
    </PageContainer>
  )
}

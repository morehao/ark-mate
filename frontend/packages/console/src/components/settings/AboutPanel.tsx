import { Spin } from 'antd'
import { tokens } from '@ark-mate/ui'
import { useMate } from '../../store'
import { CardHeader } from './CardHeader'

/**
 * 设置 · 关于：产品名、核心版本、产品身份版本与一句话定位。
 *
 * 三条**用户可见**的口径（本文件的存在理由就是它们，改文案前先读一遍）：
 *
 *  1. **不显示执行引擎**：不出现 `dsh` / `DeepSeek Harness` / `runtime` / `底座` 这类词，也没有
 *     「展开高级信息」——诊断信息在诊断面与日志里，这一节只回答"这个产品是什么"。
 *  2. **只读**：这里没有任何可写的东西。产品名与身份版本跟构建走，不提供改名（那会把它变成
 *     另一条产品线：昵称/onboarding），也不提供"切换人格"。
 *  3. **能力说明与身份分开**：末句写"能力由当前会话的工具、权限与工作区决定"——身份不是能力
 *     承诺，避免用户以为"产品是 ark-mate"等于"它什么都能干"。
 *
 * 事实来源是 `GET /meta` 的 `product` 段（`store.product`）。它取不到时**不编造版本号**：
 * 名称与定位用构建期常量（它们不随部署变），版本显示 `—` 并说明"正在读取"——
 * 一个假的 `0.1.0` 比一个"暂时读不到"更糟。
 */

const PRODUCT_NAME_FALLBACK = 'ark-mate'
const PRODUCT_TAGLINE_FALLBACK = '你的桌面个人 Agent 助手'

/** 一行"标签 + 值"（标签列固定宽，值用等宽字体：版本号读起来不抖）。 */
function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'baseline', gap: 12 }}>
      <span style={{ width: 84, flex: 'none', fontSize: 12.5, color: tokens.textSecondary }}>{label}</span>
      <span style={{ fontFamily: tokens.fontMono, fontSize: 12.5, overflowWrap: 'anywhere' }}>{value}</span>
    </div>
  )
}

export function AboutPanel() {
  const product = useMate((s) => s.product)

  const name = product?.displayName || product?.name || PRODUCT_NAME_FALLBACK
  const tagline = product?.tagline || PRODUCT_TAGLINE_FALLBACK
  const version = product?.version?.trim() || '—'
  const identityVersion = product?.identityVersion?.trim() || '—'

  return (
    <div className="fg-setsec">
      <div className="fg-setsec-head">
        <span className="fg-setsec-desc">
          这里说明你正在用的产品是什么、是哪一版。它不涉及任何可配置项——产品名与身份版本跟构建走。
        </span>
      </div>

      <div className="fg-setsec-card">
        <CardHeader
          title={name}
          hint={product == null ? '正在读取产品信息…' : tagline}
          extra={product == null ? <Spin size="small" /> : undefined}
        />
        <div style={{ padding: '16px', display: 'flex', flexDirection: 'column', gap: 12 }}>
          {/* 产品名 + 定位（大字那一块：这是这一节的主角）。 */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span style={{ fontSize: 20, fontWeight: 600, letterSpacing: 0.2 }}>{name}</span>
            <span style={{ fontSize: 12.5, color: tokens.textSecondary }}>{tagline}</span>
          </div>

          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              gap: 8,
              padding: '12px 14px',
              border: `1px solid ${tokens.border}`,
              borderRadius: tokens.radiusSm,
            }}
          >
            <Fact label="核心版本" value={version} />
            <Fact label="产品身份" value={identityVersion} />
          </div>

          {/* 身份不是能力承诺：这句是这条边界在界面上的唯一落点。 */}
          <div style={{ fontSize: 12, color: tokens.textPlaceholder, lineHeight: 1.7 }}>
            {name} 是产品；能力由当前会话的工具、权限与工作区决定。
          </div>
        </div>
      </div>
    </div>
  )
}

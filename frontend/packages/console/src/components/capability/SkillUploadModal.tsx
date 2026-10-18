import { useEffect, useState } from 'react'
import { Alert, Button, Modal, Upload } from 'antd'
import { FileZipOutlined, InboxOutlined } from '@ant-design/icons'
import { tokens } from '@ark-mate/ui'
import type { SkillWriteResp } from '@ark-mate/types'

/** 与后端 `MaxPackageBytes` 对齐：客户端先拦一次，省一次往返（服务端仍是权威）。 */
const MAX_BYTES = 20 * 1024 * 1024

/**
 * 上传技能包（D1）：`Upload.Dragger` + 客户端后缀/大小校验 + 安装。
 *
 * v1 只收 `SKILL.md`（额外的脚本/资源直接拒绝——「导入成功但附件没落盘」是静默丢失）。
 * 同名 id 由后端返回 40901 `skill_exists`，**不覆盖**；错误文案原样展示，弹窗不关。
 */
export function SkillUploadModal({
  open,
  onClose,
  onUpload,
}: {
  open: boolean
  onClose: () => void
  onUpload: (file: File) => Promise<SkillWriteResp>
}) {
  const [file, setFile] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [localErr, setLocalErr] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setFile(null)
    setBusy(false)
    setErr(null)
    setLocalErr(null)
  }, [open])

  const pick = (f: File): void => {
    setErr(null)
    if (!/\.zip$/i.test(f.name)) {
      setLocalErr('只接受 .zip 技能包')
      return
    }
    if (f.size > MAX_BYTES) {
      setLocalErr(`包大小 ${(f.size / 1024 / 1024).toFixed(1)} MiB 超过 20 MiB 上限`)
      return
    }
    setLocalErr(null)
    setFile(f)
  }

  const install = async (): Promise<void> => {
    if (!file) return
    setBusy(true)
    setErr(null)
    try {
      await onUpload(file)
      onClose()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={720}
      destroyOnHidden
      title={
        <div>
          <div style={{ fontSize: 15, fontWeight: 600 }}>上传技能</div>
          <div style={{ marginTop: 2, fontSize: 11, color: tokens.textPlaceholder }}>导入技能文件，仅本地化安装。</div>
        </div>
      }
      footer={
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <Button onClick={onClose}>取消</Button>
          <Button type="primary" disabled={!file} loading={busy} onClick={() => void install()}>
            安装技能包
          </Button>
        </div>
      }
    >
      <Upload.Dragger
        accept=".zip"
        multiple={false}
        maxCount={1}
        showUploadList={false}
        beforeUpload={(f) => {
          pick(f as File)
          return false
        }}
        style={{ background: tokens.surface }}
      >
        <p style={{ margin: 0, fontSize: 26, color: tokens.textPlaceholder }}>
          <InboxOutlined />
        </p>
        <p style={{ margin: '6px 0 0', fontSize: 12.5, color: tokens.text }}>
          把技能包拖到这里，或点击选择
        </p>
        <p style={{ margin: '3px 0 0', fontSize: 10.5, color: tokens.textPlaceholder }}>
          支持 .zip；单包不超过 20 MB；根目录或唯一一层目录内需有 SKILL.md
        </p>
      </Upload.Dragger>

      {file && (
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            marginTop: 10,
            padding: '7px 10px',
            border: `1px solid ${tokens.border}`,
            borderRadius: tokens.radiusSm,
            fontSize: 12,
          }}
        >
          <FileZipOutlined style={{ color: tokens.textSecondary }} />
          <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {file.name}
          </span>
          <span style={{ fontSize: 10.5, color: tokens.textPlaceholder }}>{(file.size / 1024).toFixed(0)} KB</span>
          <Button size="small" type="text" onClick={() => setFile(null)}>
            重选
          </Button>
        </div>
      )}

      {localErr && <Alert type="error" showIcon style={{ marginTop: 10 }} message={localErr} />}
      {err && <Alert type="error" showIcon style={{ marginTop: 10 }} message="安装失败" description={err} />}

      <Alert
        type="warning"
        showIcon
        style={{ marginTop: 10, fontSize: 11.5 }}
        message="同名 id 不会自动覆盖"
        description="已有同名技能时，需要先删除它或换一个名称。v1 只导入 SKILL.md：包里额外的脚本/资源会被拒绝，避免「导入成功但附件没落盘」。"
      />
    </Modal>
  )
}

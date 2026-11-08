import { useRef, useState } from 'react'
import { message as antdMessage } from 'antd'
import type { AttachmentMeta } from '@ark-mate/types'
import { uploadAttachment } from '@ark-mate/api'

/**
 * 待发附件的本地草稿态（v32）：上传 → 待发 chip → 发送时交出 `AttachmentMeta[]`。
 *
 * **为什么抽成 hook**：附件入口同时存在于**两套输入区**——已建会话的 `Composer` 与新建会话的
 * `StartPanel`（首轮）。两处「选文件 → 上传 → 门禁 → chip → 移除 → 随 prompt 带走」的行为必须
 * 逐字一致，否则就是又一个"同一个概念两套实现"（见 `CapabilityBar` 收口时的同一条理由）。
 * 落到 hook 而不是组件，是因为两套输入区的**版面**不同（新建页是大卡片、会话页是底栏），
 * 共享的是行为而不是 DOM。
 *
 * 刷新不保留：`pendingAtts` 只在内存；未链接的 pending 由服务端 TTL 与孤儿清扫收。
 */
export interface AttachmentDraft {
  /** 已上传、等待随消息发出的附件元数据。 */
  pendingAtts: AttachmentMeta[]
  /** 有文件正在上传（期间应禁发送/禁再点回形针）。 */
  uploading: boolean
  /** 拖拽落区高亮态（由输入卡的 dragOver/onDragLeave 维护）。 */
  dragOver: boolean
  setDragOver: (v: boolean) => void
  /** 隐藏 `<input type="file">` 的 ref（回形针按钮触发它）。 */
  fileRef: React.RefObject<HTMLInputElement | null>
  /** 上传若干文件：逐个上传，图片门禁不过则提示并跳过。 */
  addFiles: (files: FileList | File[] | null) => Promise<void>
  /** 从待发里移除一条（仅本地；不删服务端字节，交给 TTL/孤儿清扫）。 */
  removeAtt: (id: string) => void
  /** 清空待发（发送成功后调用）。 */
  clear: () => void
}

/**
 * @param imageSupported 当前路由/模型能否收图：`false` → 图片附件上传后跳过并提示；
 *   `undefined`/`true` → 不拦（交给服务端门禁兜底）。
 */
export function useAttachmentDraft(imageSupported?: boolean): AttachmentDraft {
  const [pendingAtts, setPendingAtts] = useState<AttachmentMeta[]>([])
  const [uploading, setUploading] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const fileRef = useRef<HTMLInputElement | null>(null)

  const addFiles = async (files: FileList | File[] | null) => {
    if (!files) return
    const list = Array.from(files)
    if (list.length === 0) return
    setUploading(true)
    for (const f of list) {
      try {
        const att = await uploadAttachment(f)
        // 门禁：模型不能收图且这是图片 → 上传成功但**不挂进待发**（提示并跳过，避免发出去被拒）。
        if (att.kind === 'image' && imageSupported === false) {
          void antdMessage.warning('当前模型不支持图片输入，已跳过该图片')
          continue
        }
        setPendingAtts((prev) => [
          ...prev,
          { id: att.id, name: att.name, mime_type: att.mime_type, size_bytes: att.size_bytes, kind: att.kind, file_id: att.file_id },
        ])
      } catch (err) {
        void antdMessage.error(`上传 ${f.name} 失败：${err instanceof Error ? err.message : String(err)}`)
      }
    }
    setUploading(false)
  }

  const removeAtt = (id: string) => setPendingAtts((prev) => prev.filter((a) => a.id !== id))
  const clear = () => setPendingAtts([])

  return { pendingAtts, uploading, dragOver, setDragOver, fileRef, addFiles, removeAtt, clear }
}

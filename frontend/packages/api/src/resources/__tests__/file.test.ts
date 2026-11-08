import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

// 在 import file.ts 之前先 mock 掉 request 模块：file.ts 顶层 `import { http }`，
// 只有 mock 提前生效，filePresignUrl 里调到的 http.post 才是可控的假实现。
vi.mock('../../request', () => ({
  http: {
    post: vi.fn(),
  },
}))

import { http } from '../../request'
import {
  assertFileId,
  isDshFileId,
  filePresignUrl,
  fetchFileUrl,
  type PresignResp,
} from '../file'

// 一个形状合法的 mate file_id（= core_file_upload.id，golib 生成的 UUID v7）。
const MATE_FILE_ID = '0190f2c4-7a1b-7c3d-8e9f-0123456789ab'

// 造一个「axios 风格」的 405 错误对象：resolveRequestTarget / statusOf 只读这几个字段。
function makeAxios405() {
  return {
    isAxiosError: true,
    message: 'Request failed with status code 405',
    config: { method: 'post', baseURL: '/v1', url: `/mate/files/${MATE_FILE_ID}/presign-url` },
    response: { status: 405, data: '' },
  }
}

const postMock = http.post as unknown as ReturnType<typeof vi.fn>

describe('isDshFileId / assertFileId（file_id 来源判据）', () => {
  it('接受 mate 体系的 UUID 形 file_id', () => {
    expect(isDshFileId(MATE_FILE_ID)).toBe(false)
    expect(() => assertFileId(MATE_FILE_ID)).not.toThrow()
  })

  it('拒绝带冒号命名空间的 DSH 原生 id（dsh: / dsh-file:）', () => {
    for (const id of ['dsh:abc123', 'dsh-file:abc123']) {
      expect(isDshFileId(id)).toBe(true)
      expect(() => assertFileId(id)).toThrow(/其它宿主（DSH）/)
    }
  })

  it('拒绝 file_ 前缀的 DSH 形 id', () => {
    expect(isDshFileId('file_abc123')).toBe(true)
    expect(() => assertFileId('file_abc123')).toThrow(/其它宿主（DSH）/)
  })

  it('空 / 全空白 / 非字符串一律拒绝（id 为空）', () => {
    expect(() => assertFileId('')).toThrow(/文件 id 为空/)
    expect(() => assertFileId('   ')).toThrow(/文件 id 为空/)
    // 运行时容错：类型标注是 string，但防御非字符串输入。
    expect(() => assertFileId(undefined as unknown as string)).toThrow(/文件 id 为空/)
  })
})

describe('filePresignUrl（presign 请求与错误上下文）', () => {
  const originalLocation = globalThis.location

  beforeEach(() => {
    postMock.mockReset()
    // 模拟「页面挂在 DSH Web 62005」的浏览器环境，让错误信息能还原出完整 URL（含 origin）。
    Object.defineProperty(globalThis, 'location', {
      value: { origin: 'http://127.0.0.1:62005' },
      configurable: true,
      writable: true,
    })
  })

  afterEach(() => {
    Object.defineProperty(globalThis, 'location', {
      value: originalLocation,
      configurable: true,
      writable: true,
    })
  })

  it('正常路径：原样返回 PresignResp', async () => {
    const resp: PresignResp = {
      url: 'http://127.0.0.1:10001/objects/abc?sig=x',
      method: 'GET',
      headers: {},
      expiresIn: 300,
    }
    postMock.mockResolvedValue(resp)

    await expect(filePresignUrl(MATE_FILE_ID)).resolves.toEqual(resp)
    expect(postMock).toHaveBeenCalledWith(`/mate/files/${MATE_FILE_ID}/presign-url`)
  })

  it('请求 405 时：错误信息含 “HTTP 405” 与完整请求 URL（含 origin）', async () => {
    postMock.mockRejectedValue(makeAxios405())

    const err = await filePresignUrl(MATE_FILE_ID).then(
      () => null,
      (e: unknown) => e as Error,
    )
    expect(err).toBeInstanceOf(Error)
    const msg = err!.message
    expect(msg).toContain('HTTP 405')
    expect(msg).toContain(`POST http://127.0.0.1:62005/v1/mate/files/${MATE_FILE_ID}/presign-url`)
    expect(msg).toContain('请改用核心 origin')
  })

  it('DSH 形 file_id 在发请求前就被挡下（打不到 http.post）', async () => {
    await expect(filePresignUrl('dsh-file:abc123')).rejects.toThrow(/其它宿主（DSH）/)
    expect(postMock).not.toHaveBeenCalled()
  })

  it('网络错误（无状态码）不说“打错后端”：给诚实的“后端可能未启动/重启”文案', async () => {
    // 模拟后端重启/连接被 reset：axios 错误无 response.status。
    postMock.mockRejectedValue({
      isAxiosError: true,
      message: 'Network Error',
      config: { method: 'post', baseURL: '/v1', url: `/mate/files/${MATE_FILE_ID}/presign-url` },
      // 无 response 字段
    })

    const err = await filePresignUrl(MATE_FILE_ID).then(
      () => null,
      (e: unknown) => e as Error,
    )
    expect(err).toBeInstanceOf(Error)
    const msg = err!.message
    expect(msg).toContain('网络错误')
    // 不能再把网络错误归因为“打错后端 origin”。
    expect(msg).not.toContain('请改用核心 origin')
    expect(msg).not.toContain('该路径不是本应用接口')
  })
})

describe('fetchFileUrl（兼容：内部仍走 filePresignUrl）', () => {
  it('返回签名 url 字符串', async () => {
    postMock.mockReset()
    postMock.mockResolvedValue({
      url: 'http://127.0.0.1:10001/objects/xyz',
      method: 'GET',
      headers: {},
      expiresIn: 300,
    } satisfies PresignResp)
    await expect(fetchFileUrl(MATE_FILE_ID)).resolves.toBe('http://127.0.0.1:10001/objects/xyz')
  })
})

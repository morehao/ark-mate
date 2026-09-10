/**
 * 工作空间（= 会话的 ACP cwd）相关的只读数据面类型。
 *
 * 服务端契约见 README「控制面接口摘要」：`GET /workspaces/browse`、`GET /workspaces/recent`。
 * 语义要点：工作空间必须是**目录**，且服务端返回的是**符号链接规范化后的绝对路径**——
 * 前端把 `entries[].path` 直接作为建会话的 `cwd` 提交即可命中同一会话分组，不要自行拼接/比较路径。
 */

/** 浏览结果中的一个候选子目录。 */
export interface WorkspaceEntry {
  /** 目录名（不含路径）。 */
  name: string
  /** 绝对路径（可直接作为建会话的 cwd）。 */
  path: string
}

/** `GET /workspaces/browse` 响应。 */
export interface WorkspaceBrowseResp {
  /** 规范化后的当前目录绝对路径。 */
  path: string
  /** 上级目录（到达文件系统根**或已配置的工作区根**时缺省）。 */
  parent?: string
  /** 服务进程家目录（"回到主目录"用；不可得或不在工作区根内时缺省）。 */
  home?: string
  /**
   * 生效的工作区根白名单（服务端 `FORGE_WORKSPACE_ROOTS`；空 = 不限制）。
   * 服务端会把 `cwd` 与浏览都收在根内，故前端应当把根列成入口——
   * 既让"为什么上不去了"有解释，也让用户一进来就看到允许工作的位置。
   */
  roots?: string[]
  entries: WorkspaceEntry[]
}

/**
 * 最近使用过的工作空间（从既有会话派生）。
 * 排除平台代建的临时空间——那是某个会话独占的一次性目录，不具备复用价值。
 */
export interface WorkspaceRecent {
  path: string
  name: string
  /** 该工作空间下最新会话的 updated_at（ISO RFC3339）。 */
  last_used_at: string
  /** 该工作空间下的会话数（含归档）。 */
  conversations: number
}

/**
 * 资料库 = 工作区根（`FORGE_WORKSPACE_ROOTS`）下的一级子目录。
 *
 * 服务端**零新状态**：不存登记表，目录名即资料库名（`GET /workspaces/libraries`）。
 * 未配 `FORGE_WORKSPACE_ROOTS` 时服务端返回空数组——资料库是"策展"能力，
 * 前提是运维先把允许的工作区画出来；此时面板只保留"浏览目录"入口。
 */
export interface WorkspaceLibrary {
  /** 绝对路径（可直接作为建会话的 cwd）。 */
  path: string
  /** 目录名即资料库名。 */
  name: string
  /** 所属工作区根。 */
  root: string
  /** 该资料库下最新会话的 updated_at；从未用过则缺省。 */
  last_used_at?: string
  /** 该资料库下的会话数（含归档）。 */
  conversations: number
}

/** 目录内容中的一项（文件或子目录）。 */
export interface WorkspaceFileEntry {
  name: string
  path: string
  is_dir: boolean
  /** 字节数（符号链接取真身大小）。 */
  size: number
  /** 最后修改时间（RFC3339，UTC）。 */
  modified_at: string
}

/**
 * `GET /workspaces/files` 响应（"看资料"面）。
 *
 * 与 `WorkspaceBrowseResp` 的分工：browse 用于**选工作空间**（只列目录，`entries[].path`
 * 可直接当 cwd），files 用于**看内容**（列文件 + 目录）。因此这里的 `entries[].path`
 * **不可**作为建会话的 cwd——文件不是合法工作空间。
 */
export interface WorkspaceFilesResp {
  path: string
  roots?: string[]
  /** 目录在前、文件在后，各自按名升序。 */
  entries: WorkspaceFileEntry[]
}

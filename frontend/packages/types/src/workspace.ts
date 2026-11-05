import type { UnixMillis } from './time'

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
  /**
   * 宿主约定的隐藏项（POSIX 点前缀）。
   *
   * 服务端在 `hidden=false` 时**不下发**这些行；下发时带标记，供面板自己做「显示隐藏文件」
   * 开关（切换开关不必重发请求——隐藏行已在手上）——对齐 dsh 的 `DirectoryEntry.hidden`。
   */
  hidden?: boolean
}

/**
 * 面包屑的一项（v14.3）：一个祖先目录，逐级可跳。
 *
 * 服务端下发**从文件系统根到当前目录（含自身）**的完整链，前端不按路径字符串切分——
 * 盘符 / UNC / 符号链接（macOS 的 `/var` → `/private/var`）三处都会切错。
 * 配了白名单时链首是**离当前目录最近的那个根**（根外的祖先既不合法也只是噪音）。
 */
export interface WorkspaceCrumb {
  /** 显示名（文件系统根用它自身的路径当名字，例如 `/`；盘符形如 `C:\`）。 */
  name: string
  /** 绝对路径（跳转目标）。 */
  path: string
  /** 恒为 false（祖先链上不区分隐藏与否——那是名字的宿主约定，不是位置的属性）。 */
  hidden?: boolean
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
   * 面板配置的**起始点偏好**（服务端 `MATE_WORKSPACE_ROOTS`；空 = 没有偏好）。
   *
   * **它不是"允许范围"**：任何已存在的目录都能浏览、都能当工作空间（选工作区是全系统）。
   * 面板把这几项列成快捷入口，省去"从家目录一层层点下去"。
   */
  roots?: string[]
  /** 文件系统根 → 当前目录的祖先链（含自身），逐级可跳；始终非空。 */
  crumbs?: WorkspaceCrumb[]
  /** 单层子目录超过服务端上限时被截断（界面据此说"还有未显示的子目录"）。 */
  truncated?: boolean
  entries: WorkspaceEntry[]
}

/** `POST /workspaces/dirs` 响应：新建目录的规范化绝对路径（可直接当 cwd / 加入白名单）。 */
export interface WorkspaceDirCreateResp {
  path: string
}

/**
 * 一条**工作空间登记**（v14；`GET /workspaces` 的 items）。
 *
 * 与 `WorkspaceRecent` 的区别是本轮设计的核心：那一个是**派生的只读视图**（从既有会话算出来的），
 * 这一条是**用户登记下来的持久实体**——
 * 有 id、有标题、有顺序，可以被改写与删除。选择器的主列表取这一份。
 */
export interface Workspace {
  /** 平台 uuid（对外稳定标识；改名/搬迁不改它）。会话的 `workspace_id` 指向它。 */
  id: string
  /** 规范化后的绝对路径（可直接作为建会话的 cwd）。 */
  path: string
  /** 展示名（'' = 未命名，按末级目录名渲染；默认工作空间读作「默认工作空间」）。 */
  title: string
  /** 是否平台默认工作空间（删不掉；「未指定 cwd 的建会话」落在它上面）。 */
  is_default: boolean
  /** 展示顺序（升序）。 */
  list_order: number
  /** 目录现状（**读时现算不缓存**）：ok | missing | not_dir | unreadable。 */
  dir_status: WorkspaceDirStatus
  /** 该工作空间名下的会话数（含归档；历史会话算进默认工作空间那一行）。 */
  sessions: number
  /** 最近使用时间（Unix 毫秒；缺省 = 从未用过）。 */
  last_used_at?: UnixMillis
  created_at: UnixMillis
  updated_at: UnixMillis
}

/** 目录现状。非 `ok` 时这一行**仍然在下发**（登记不会因为目录消失就凭空不见），只是不能在其中建会话。 */
export type WorkspaceDirStatus = 'ok' | 'missing' | 'not_dir' | 'unreadable'

/** `GET /workspaces` 响应。 */
export interface WorkspaceListResp {
  items: Workspace[]
  /** 默认工作空间 id（缺省 = 该部署没有默认工作空间，例如代建关闭）。 */
  default_id?: string
  /**
   * 是否可创建/改名/删除（memory 持久层 = false）。
   * false 时界面**不渲染**创建与管理入口——不给一个必然失败的按钮。
   */
  writable: boolean
  /** 平台默认工作空间的所在目录（缺省 = 代建关闭）。 */
  provision_dir?: string
  /**
   * **新建工作空间的落盘根**（缺省 = 未声明）。
   *
   * 它与 `provision_dir` 今天取同一个值，但是两件事：那个是"平台代建的默认工作空间落哪"，
   * 这个是"用户新建的工作空间落哪"。
   *
   * 缺省时界面**不渲染**「新建工作空间…」入口——不给一个必然失败的按钮（ADR-3 的入口纪律，
   * 同 ADR-11）。
   */
  create_root?: string
}

/** `POST /workspaces` 请求体（登记一个已存在的目录）。 */
export interface WorkspaceCreateReq {
  path: string
  /** 展示名（可选；空 = 未命名）。**不是身份**：改名不改 id、不改路径。 */
  title?: string
  /** true = 路径已登记时报错（40901）；缺省 = get-or-create 返回既有那一行。 */
  strict?: boolean
}

/** `DELETE /workspaces/:id` 响应。 */
export interface WorkspaceDeleteResp {
  id: string
  /** 归属被清空、读面归到默认工作空间的会话数（会话本身与 cwd 均未改动）。 */
  released_sessions: number
}

/** `POST /workspaces/:id/move` 响应：重排后的**完整**顺序（客户端据此对齐本地顺序）。 */
export interface WorkspaceMoveResp {
  ids: string[]
}

/**
 * 最近使用过的工作空间（从既有会话派生）。
 *
 * v14 时它是选择器的一栏；**v14.3 起面板已无消费者**（选择面只剩登记项 + 「添加工作空间…」，
 * 对齐 dsh）。类型保留是因为 `GET /workspaces/recent` 仍在（README 的接口摘要里有它），
 * 而契约类型应当跟着端点走——删类型不删端点会让下一个人以为那个端点没了。
 */
export interface WorkspaceRecent {
  path: string
  name: string
  /** 该工作空间下最新会话的 updated_at（Unix 毫秒）。 */
  last_used_at: UnixMillis
  /** 该工作空间下的会话数（含归档）。 */
  sessions: number
}

/** 目录内容中的一项（文件或子目录）。 */
export interface WorkspaceFileEntry {
  name: string
  path: string
  is_dir: boolean
  /** 字节数（符号链接取真身大小）。 */
  size: number
  /** 最后修改时间（Unix 毫秒，UTC）。 */
  modified_at: UnixMillis
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


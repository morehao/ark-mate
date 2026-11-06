import { Dropdown } from 'antd'
import { CloseOutlined, FolderOutlined, SearchOutlined, UnorderedListOutlined } from '@ant-design/icons'
import type { MenuProps } from 'antd'
import {
  RAIL_ARCHIVED_FILTERS,
  RAIL_GROUP_MODES,
  railArchivedFilterLabel,
  railGroupModeLabel,
  type RailArchivedFilter,
  type RailGroupMode,
} from '../console/workspaceView'
import { AddWorkspaceButton } from './AddWorkspaceButton'

/**
 * 「视图选项」的触发器图标：**两条横轨 + 两个圆旋钮**。
 *
 * 这里不用 antd 的 `SlidersOutlined`：那是**三根竖推子**，与参考实现 dsh 的
 * `IconSlidersTwoOutlineRegular`（两条横轨各一个圆旋钮，16×16 / 1px 描边 / currentColor）
 * 不是一个形状。dsh 源码是 MIT，但本仓没有 THIRD_PARTY_NOTICES 的收口位置，
 * 所以按**同一构造自己画**，而不是把 4 段 SVG 路径逐字搬进来。
 */
function SlidersTwoIcon({ size = 16 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.2}
      strokeLinecap="round"
      aria-hidden="true"
    >
      <path d="M2.3 5h5.6M12.2 5h1.5" />
      <circle cx="9.6" cy="5" r="1.5" />
      <path d="M2.3 11h1.5M8 11h5.7" />
      <circle cx="5.9" cy="11" r="1.5" />
    </svg>
  )
}

interface Props {
  /** 搜索框里的草稿（受控；判定与请求在 `SessionPane`）。 */
  query: string
  onQueryChange: (value: string) => void
  /** 搜索是否展开。折叠时只占一枚图标，展开后**吃掉整条区头**。 */
  searching: boolean
  onSearchingChange: (value: boolean) => void
  groupMode: RailGroupMode
  onGroupModeChange: (mode: RailGroupMode) => void
  /** 登记表是否可用（null/空 = 不可用）：不可用时按工作空间分组没有东西可挂。 */
  canGroup: boolean
  archivedFilter: RailArchivedFilter
  onArchivedFilterChange: (filter: RailArchivedFilter) => void
  /** 登记表是否可写：不可写（memory 持久层）时不渲染「添加工作区」——不留必然失败的按钮（ADR-11）。 */
  writable: boolean
}

/**
 * 左栏「工作区」区头（v14.2）。
 *
 * 结构对齐 dsh 的 `sectionHeader`：**标题 + 折叠搜索 + 动作两颗**，高 36px。
 * 它取代了原先的两排东西：`会话 / 归档` 与 `工作空间 / 时间线` 两组分段切换（收进「视图选项」菜单）、
 * 以及列表中上方那条**常驻**的搜索框（收成区头里一枚可展开的图标）。
 *
 * 三条都是"少占一行、把常驻控件换成按需展开"，代价是这两个维度不再一眼可见——所以菜单项
 * 必须**打勾**（`selectable`）：用户总得有个地方能确认"我现在看的是哪种视图"。
 *
 * 搜索展开时标题与动作两颗一起收起（`fg-searching`）：不是动画效果，而是让输入框拿到整条区头的
 * 宽度——294px 的左栏里，留着一颗标题和两颗按钮的话，输入框只剩不到 100px，等于搜不了。
 */
export function RailSectionHeader({
  query,
  onQueryChange,
  searching,
  onSearchingChange,
  groupMode,
  onGroupModeChange,
  canGroup,
  archivedFilter,
  onArchivedFilterChange,
  writable,
}: Props) {
  const viewItems: MenuProps['items'] = [
    {
      type: 'group',
      // 登记表不可用时把原因写在组标题上：只把一个选项置灰、不说为什么，等于让用户自己猜。
      label: canGroup ? '分组方式' : '分组方式 · 登记表不可用',
      children: RAIL_GROUP_MODES.map((m) => ({
        key: `group:${m}`,
        label: railGroupModeLabel(m),
        // 「按工作空间」= 文件夹，「时间线」= 无序列表（三条横线 + 三个点）。
        // 后者**不能**用时钟：时钟在参考实现里是「排序方式 → 最近更新」，摆在这里等于把
        // "怎么分组"和"按什么排序"两个维度画成同一个符号。
        icon: m === 'workspace' ? <FolderOutlined /> : <UnorderedListOutlined />,
        disabled: m === 'workspace' && !canGroup,
      })),
    },
    { type: 'divider' },
    {
      type: 'group',
      label: '筛选会话',
      children: RAIL_ARCHIVED_FILTERS.map((f) => ({
        key: `filter:${f}`,
        label: railArchivedFilterLabel(f),
      })),
    },
  ]

  const closeSearch = () => {
    onQueryChange('')
    onSearchingChange(false)
  }

  const archivedOnly = archivedFilter === 'only'

  return (
    <>
      <div className={`fg-rail-sechead${searching ? ' fg-searching' : ''}`}>
      <span className="fg-rail-sechead-label">工作区</span>

      <div className="fg-rail-sesearch">
        <div className="fg-rail-sesearch-box">
          <button
            type="button"
            className="fg-rail-sechead-icon fg-searchbtn"
            title="搜索会话"
            aria-label="搜索会话"
            onClick={() => onSearchingChange(true)}
          >
            <SearchOutlined />
          </button>
          <input
            className="fg-rail-seinput"
            value={query}
            placeholder="搜索会话名称"
            aria-label="搜索会话名称"
            onChange={(e) => onQueryChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape') closeSearch()
            }}
          />
          <button
            type="button"
            className="fg-rail-sechead-icon fg-rail-seclear"
            title="清除搜索"
            aria-label="清除搜索"
            onClick={closeSearch}
          >
            <CloseOutlined />
          </button>
        </div>
      </div>

      <div className="fg-rail-sechead-actions">
        <Dropdown
          menu={{
            items: viewItems,
            // 打勾 = 用户唯一能确认"现在看的是哪种视图"的地方（分段切换下线后的必要补偿）。
            selectable: true,
            selectedKeys: [`group:${groupMode}`, `filter:${archivedFilter}`],
            onClick: ({ key }) => {
              if (key.startsWith('group:')) onGroupModeChange(key.slice('group:'.length) as RailGroupMode)
              else if (key.startsWith('filter:')) {
                onArchivedFilterChange(key.slice('filter:'.length) as RailArchivedFilter)
              }
            },
          }}
          trigger={['click']}
          placement="bottomRight"
        >
          <button type="button" className="fg-rail-sechead-icon" title="视图选项" aria-label="视图选项">
            <SlidersTwoIcon />
          </button>
        </Dropdown>
        {writable && <AddWorkspaceButton />}
      </div>
      </div>
      <div className="fg-rail-tabs" role="tablist" aria-label="会话视图">
        <button
          type="button"
          role="tab"
          aria-selected={!archivedOnly}
          className={`fg-rail-tab${archivedOnly ? '' : ' fg-on'}`}
          onClick={() => onArchivedFilterChange('hide')}
        >
          会话
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={archivedOnly}
          className={`fg-rail-tab${archivedOnly ? ' fg-on' : ''}`}
          onClick={() => onArchivedFilterChange('only')}
        >
          归档
        </button>
      </div>
    </>
  )
}

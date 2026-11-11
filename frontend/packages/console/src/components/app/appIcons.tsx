// 应用图标：内置图标键 → @ant-design/icons 静态映射（不做远程图，免外链与追踪）。
//
// 原型里的 emoji（☁ 📰 📄 …）只是占位，正式 UI 用语义接近的线性图标。键集合沿用市场清单
// `icon` 字段（weather/digest/doc/code/spark/chart/pen/search/briefcase/library…），
// 命中不了时退回通用 AppstoreOutlined（fail-open 到中性图标，不丢卡片）。
import type { ReactNode } from 'react'
import {
  AppstoreOutlined,
  BarChartOutlined,
  BookOutlined,
  CloudOutlined,
  CodeOutlined,
  FileTextOutlined,
  ProfileOutlined,
  ReadOutlined,
  SearchOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'

const ICON_MAP: Record<string, ReactNode> = {
  weather: <CloudOutlined />,
  digest: <ProfileOutlined />,
  doc: <FileTextOutlined />,
  code: <CodeOutlined />,
  spark: <ThunderboltOutlined />,
  chart: <BarChartOutlined />,
  pen: <ReadOutlined />,
  search: <SearchOutlined />,
  briefcase: <BookOutlined />,
  library: <BookOutlined />,
}

/** 取内置图标键对应的图标节点；未知键退回中性图标。 */
export function appIconNode(key?: string): ReactNode {
  if (key && ICON_MAP[key]) return ICON_MAP[key]
  return <AppstoreOutlined />
}

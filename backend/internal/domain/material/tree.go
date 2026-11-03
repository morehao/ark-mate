package material

// WouldCycle 判断「把节点 movingID 移到 newParentID 之下」是否会让目录树成环。
//
// ancestors 是目标父节点 newParentID 的**祖先链**（从 newParentID 自身一路向上到根，含 newParentID）。
// 若 movingID 出现在这条链上，说明「目标父节点」其实是 movingID 自己或它的子孙——移动后会形成
// 环（A 的父是 B、B 的父是 A），读取时无限递归。写面必须在移动前调用本函数，成环报 40001
// `material_cycle`（fail-closed：宁可拒绝也不写出一棵坏树）。
//
// 纯函数：不查库、不改状态；调用方负责先把 ancestors 采集好（由用例层沿 parent_id 向上取）。
func WouldCycle(movingID string, newParentID string, ancestors []string) bool {
	if movingID == "" {
		return false
	}
	// 移到根（空父）永不成环。
	if newParentID == "" {
		return false
	}
	// 直接把自己当父 = 成环。
	if newParentID == movingID {
		return true
	}
	for _, a := range ancestors {
		if a == movingID {
			return true
		}
	}
	return false
}

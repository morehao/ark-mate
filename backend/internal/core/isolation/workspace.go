package isolation

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkspaceRoot 按「租户/用户/会话」组织工作区 cwd 根：
// `<base>/workspaces/<tenantID>/<userID>/<sessionSlug>`（见 C2「落地方式」）。
//
// base 通常是数据根（如 `$MATE_HOME`）。三个标识都必须是**单段**（不含路径分隔符、非 `.`/`..`）：
// 任一非法即返回 error——`../` 之类注入会把别人的工作区当成本用户的，那是跨租户越权写（不可逆），
// 所以这里**fail-closed**，绝不拼接一个"看起来能用"的危险路径。
func WorkspaceRoot(base, tenantID, userID, sessionSlug string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", fmt.Errorf("workspace root: base 为空")
	}
	for _, part := range []struct{ name, val string }{
		{"tenantID", tenantID}, {"userID", userID}, {"sessionSlug", sessionSlug},
	} {
		if !SingleSegment(part.val) {
			return "", fmt.Errorf("workspace root: %s=%q 不是合法单段路径（不得含分隔符/./..）", part.name, part.val)
		}
	}
	return filepath.Join(base, "workspaces", tenantID, userID, sessionSlug), nil
}

// SingleSegment 判断 s 是否为单个安全路径段：非空、非 `.`/`..`、不含 `/` 或 `\`。
func SingleSegment(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return false
	}
	return !strings.ContainsAny(s, `/\`)
}

package sessionruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtosession"
)

// TestCreateSessionRejectsInvalidPermissionMode v33 验收：`permission_mode` 必传三档之一，
// 缺键 / 空串 / 非法值一律 40001，且**在任何副作用之前**拒（不拉进程、不写库、不登记会话）。
//
// 为什么这条必须有：dsh adapter 对未知模式是**静默不注入**（不报错、不设沙箱），所以漏校验
// 的表现是"用户以为设了只读、实际跑在完全放行/不注入的进程上"——静默且危险。
func TestCreateSessionRejectsInvalidPermissionMode(t *testing.T) {
	svc, orch, _ := validateRig(t)
	ctx := context.Background()

	// 刻意**不**建默认工作空间：这样合法档会走到 workspace_required，非法档会更早被
	// invalid_permission_mode 拦下——用错误码即可证明"校验发生在工作空间解析之前"。
	cases := []struct {
		name string
		mode string
	}{
		{"缺键/空串", ""},
		{"非法值", "auto"},    // 平台内部值，不对用户面开放
		{"非法值 off", "off"}, // 同上
		{"大小写/拼写错", "Read-Only"},
		{"未知名", "superuser"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{PermissionMode: c.mode})
			if err == nil {
				t.Fatalf("permission_mode=%q 应被拒", c.mode)
			}
			if got := numericCode(err); got != 40001 {
				t.Fatalf("permission_mode=%q 的码 = %d，期望 40001（%v）", c.mode, got, err)
			}
			if !strings.Contains(err.Error(), "permission_mode") {
				t.Fatalf("permission_mode=%q 的错误文案应点明 permission_mode：%v", c.mode, err)
			}
			if n := orch.pool.Len(); n != 0 {
				t.Fatalf("校验失败不该建立任何连接，got %d", n)
			}
			if list, _ := sessionPlaneOf(svc).ListSessions(false, "", ""); len(list) != 0 {
				t.Fatalf("校验失败不该登记会话，got %+v", list)
			}
		})
	}
}

// TestCreateSessionAcceptsAllThreePermissionModes 三档合法值必须通过权限校验。
// 用"没有默认工作空间 → workspace_required"这一后续错误证明它**越过了**权限校验那一关
// （而不是被 invalid_permission_mode 拦下）。
func TestCreateSessionAcceptsAllThreePermissionModes(t *testing.T) {
	svc, _, _ := validateRig(t)
	ctx := context.Background()

	for _, mode := range []string{"read-only", "workspace-write", "danger-full-access"} {
		_, err := svc.CreateSession(ctx, dtosession.SessionCreateReq{PermissionMode: mode})
		if err == nil {
			// 没有默认工作空间时本该报 workspace_required；放行到这里说明夹具变了，需修测。
			t.Fatalf("permission_mode=%q：预期因缺默认工作空间而失败，却成功了（夹具变了？）", mode)
		}
		if strings.Contains(err.Error(), "permission_mode") {
			t.Fatalf("合法档 permission_mode=%q 被误判为非法：%v", mode, err)
		}
	}
}

// Package isolation 定义「工作区隔离等级」的纯词汇与判定（SaaS 池化 M4-1 / 挑战点 C2）。
//
// 本包只做**纯判定与路径构造**（不碰磁盘、不 import 业务）：隔离档的分级、可比性、以及
// 「实际 backend 是否满足声明的隔离要求」——这是反目标 2「声明 sandboxed 永不静默降 os_user」
// 的唯一判决点。真正的隔离执行（os 账户 / pod / gVisor runsc）在部署层，本包不实现。
package isolation

import "fmt"

// Level 工作区隔离等级，强度严格递增：none < os_user < sandboxed。
//
// 与 C2 的分档一一对应：
//   - none      ：一个 cwd 根给所有人——只做 demo，**生产禁用**（一次 `rm -rf`/读 `.env` 即跨租户）。
//   - os_user   ：每用户一个 OS 账户 + 独立 HOME/DSH_HOME + 工作区根；默认生产档（低风险租户）。
//   - sandboxed ：每用户一 pod，fs/subprocess seam 指向 pod 内，宿主文件系统不可见（跑不可信代码）。
type Level string

const (
	LevelNone      Level = "none"
	LevelOSUser    Level = "os_user"
	LevelSandboxed Level = "sandboxed"
)

// rank 等级的强度序（越大越强）；未知等级返回 -1（fail-closed：不认识的档一律判无效）。
func (l Level) rank() int {
	switch l {
	case LevelNone:
		return 0
	case LevelOSUser:
		return 1
	case LevelSandboxed:
		return 2
	default:
		return -1
	}
}

// Valid 等级是否是合法档位。
func (l Level) Valid() bool { return l.rank() >= 0 }

// ParseLevel 解析字符串为等级；非法/空一律返回 error（不静默回落任何默认档——降级是不可逆的越权风险）。
func ParseLevel(s string) (Level, error) {
	l := Level(s)
	if !l.Valid() {
		return "", fmt.Errorf("非法隔离等级 %q（合法：none | os_user | sandboxed）", s)
	}
	return l, nil
}

// Satisfies 判断「worker 实际上报的 backend」是否满足「租户声明的隔离要求」。
//
// 判据：actual 的强度必须 **>=** declared。**实际低于声明一律不满足**——这是反目标 2 的落点：
// 声明 sandboxed 的租户，worker 实际只做到 os_user（甚至 none）时，上层必须**拒启该会话并告警**，
// 绝不降级启动（降级=不可逆的越权风险）。
//
// fail-closed：任一档非法 → 判不满足（宁可拒启，不可放过）。
func Satisfies(declared, actual Level) bool {
	d, a := declared.rank(), actual.rank()
	if d < 0 || a < 0 {
		return false
	}
	return a >= d
}

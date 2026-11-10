package apppkg

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// ——— 分发形态（WP-C8）———
//
// **本期（二期 C）的分发形态就是「本地目录」**：操作者把包目录手动放进 `<应用根>/<app-id>/`，
// 平台在启动/刷新时扫描该目录装载。没有远端市场、没有签名链、没有自动更新——这三件在 C 期
// **明确不做**（设计 §17.3 + endgame：单机单操作员不建远端市场；签名链是「第三方作者身份」的
// 前提，留给未来另立决策）。这些「不做」写在这里，是为了让下一个人知道它们不是漏做，是边界。
//
// 分发的**全部逻辑**就是一条流水线（见 Distribute）：
//
//	扫描目录 → 逐包解析+校验清单 → 完整性(sha256)校验 → min core version 门槛 → 汇总
//
// 前两步（扫描/解析/licv 完整性）复用 C1 的 Scan；C8 在其上加**版本门槛**并把被判出局的包
// 从 Loaded 移到 Failed（原因明示），产出同一个 Distribution 形状供装配根消费。
//
// 完整性与版本门槛都被抽成**可复用的分发前步骤**（VerifyEntrypointIntegrity / CheckMinCoreVersion），
// 便于 C3 的 host 子进程在真正拉起陌生代码前**再校一次**（防「扫描后、执行前」之间包被替换的窗口）。

// Distribution 是一次分发的最终结果：通过全部关口、可装载的包 + 被拒/失败的包（各带原因）。
//
// 它是 ScanResult 在「加上版本门槛后」的视图：原 ScanResult.Failed 里的包原样保留，另把
// 「扫描通过但核心版本不够」的包追加进 Failed 并从 Loaded 移除。
type Distribution struct {
	// Loaded 通过 扫描 + 完整性 + 版本门槛 的包（可装载）。
	Loaded []*Package
	// Failed 未通过任一关的包（目录 + 可读原因）。失败隔离：单包失败不影响其它。
	Failed []FailedPackage
}

// Distribute 执行一次完整分发：扫描 dir、复用 C1 校验与完整性、再施加 min core version 门槛，
// 以 coreVersion 为准判定版本兼容。返回 Distribution（单包失败不返回 error；error 仅用于"扫描根
// 目录本身不可读"这类整体失败）。
//
// coreVersion 由调用方给（装配期传 `CurrentCoreVersion()`；测试注入确定版本）。传空串会令所有
// **声明了** min_core_version 的包 fail-closed 失败（核心版本说不清），未声明门槛的包不受影响。
func Distribute(dir, coreVersion string) (*Distribution, error) {
	scan, err := Scan(dir)
	if err != nil {
		return nil, err
	}
	dist := &Distribution{
		Loaded: make([]*Package, 0, len(scan.Loaded)),
		Failed: append([]FailedPackage(nil), scan.Failed...),
	}
	for _, pkg := range scan.Loaded {
		if err := CheckMinCoreVersion(pkg.Manifest, coreVersion); err != nil {
			// 版本门槛不过：从可装载集合出局，进 Failed 并明示原因（不静默跳过）。
			dist.Failed = append(dist.Failed, FailedPackage{Dir: pkg.Dir, Reason: err.Error()})
			continue
		}
		dist.Loaded = append(dist.Loaded, pkg)
	}
	return dist, nil
}

// DistributeDefault 是 Distribute(默认目录, 当前核心版本) 的便捷封装（装配根启动扫描用）。
//
// 注意：它**不缓存**、不常驻——「本地目录手动放入包 + 重启扫描」是本期唯一的更新方式（自动更新
// 留 [增强项]）。要刷新就再调一次。
func DistributeDefault() (*Distribution, error) {
	dir, err := DefaultAppsDir()
	if err != nil {
		return nil, err
	}
	return Distribute(dir, CurrentCoreVersion())
}

// VerifyEntrypointIntegrity 校验单个包 entrypoint 文件的 sha256 是否与清单一致——**可复用的分发前
// 完整性步骤**（与 load.go 里 loadOne 的判定同口径，供需要在「已装载之后、执行之前」再校一次的场景
// 复用，如 C3 host 拉起前、C6 重装前）。不一致/不可读/越界 → 返回 error（fail-closed）。
//
// 之所以单列一个函数而不是让调用方自己算：sha256 比对「大小写、hex 编码、路径越界」这些细节容易
// 各写一套而漂移；集中一处，谁要校验都调它。
func VerifyEntrypointIntegrity(pkgDir string, m *Manifest) error {
	if m == nil {
		return errIntegrity("清单为空")
	}
	epPath := filepath.Join(pkgDir, filepath.FromSlash(m.Entrypoint))
	if !withinDir(pkgDir, epPath) {
		return errIntegrity("entrypoint 解析后越出包根")
	}
	content, err := os.ReadFile(epPath)
	if err != nil {
		return errIntegrity("entrypoint 不可读: " + err.Error())
	}
	sum := sha256.Sum256(content)
	got := hex.EncodeToString(sum[:])
	if !equalHexFold(got, m.Integrity.SHA256) {
		return errIntegrity("entrypoint sha256 不符（清单声明 " + m.Integrity.SHA256 + "，实际 " + got + "）——包被篡改或未正确打包")
	}
	return nil
}

// integrityError 是完整性校验失败的专用错误类型（便于调用方 errors.As 区分「篡改」与「其它」）。
type integrityError struct{ msg string }

func (e *integrityError) Error() string { return "apppkg 完整性校验失败：" + e.msg }

func errIntegrity(msg string) error { return &integrityError{msg: msg} }

// equalHexFold 大小写不敏感地比较两个十六进制串（sha256 规范是小写，但契约上容忍大小写差异）。
func equalHexFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

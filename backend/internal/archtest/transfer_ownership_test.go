package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transferEntryAnchors 存量定义导入/导出的六个域入口（3 域 × 2 方向）。
//
// 它们必须且只能出现在 `internal/drivers/transfer`：2026-10-20 之前同一件事分居两包
// （capability 的导入留在 `mechanism`，automation/provider 的在
// `capabilityio`），改一条纪律要改两处，而两处都编译得过——「幂等 / 绝不覆盖 /
// 不回写」只要有一份没跟上，就是静默的数据语义分叉。
var transferEntryAnchors = []string{
	"func ImportCapabilityDir(",
	"func ExportCapabilityDir(",
	"func ImportAutomationDir(",
	"func ExportAutomationDir(",
	"func ImportProviderFile(",
	"func ExportProviderFile(",
}

// transferFrameworkAnchors 域无关的导入/导出原语。与入口同包：框架在 A、编排在 B 时，
// 三条纪律最容易被抄成两份（一份写 marker、另一份忘了；一份按行判重、另一份直接覆盖）。
var transferFrameworkAnchors = []string{
	"func ImportDefinitionRows(",
	"func CountDryRun(",
	"func WriteExportRows(",
	"type DefinitionImportOptions struct",
	"type DefinitionImportStats struct",
	"type DefinitionExportStats struct",
}

// TestDefinitionTransferHasOneHome 定义导入/导出只能有一个家。
//
// 判据三段：① 六入口与框架原语全在 `internal/drivers/transfer`（改名/搬走即失锚）；
// ② 旧目录 `internal/capabilityio` 不得复活（它与 `internal/core/capability` 只差后缀，
// 正是这次要消灭的歧义）；③ `internal/drivers/mechanism` 与 `domain/**` 零命中（第二个家）。
// 2026-10-21 起 mechanism 的定义/凭据两个子包合并为 `mechanism` 本包，扫描根随之从
// `mechanism` 改为 `mechanism`——判据方向不变，扫描面反而更严（覆盖了原先
// 只扫 `definition` 子目录时漏掉的同包其余文件）。
// fail-closed：transfer 目录扫不到生产 Go 文件时 `dirProductionBody` 直接 Fatal。
func TestDefinitionTransferHasOneHome(t *testing.T) {
	root := backendRoot(t)
	body := dirProductionBody(t, filepath.Join(root, "internal", "drivers", "transfer"))
	for _, a := range append(append([]string{}, transferEntryAnchors...), transferFrameworkAnchors...) {
		if !strings.Contains(body, a) {
			t.Errorf("internal/drivers/transfer 里找不到 %q：入口/框架被改名或搬走了？（判据失锚＝空转）", a)
		}
	}

	// 旧名不得复活：`capability` 是能力数据面（L1），存量定义搬运不是它的 I/O。
	if _, err := os.Stat(filepath.Join(root, "internal", "capabilityio")); err == nil {
		t.Error("internal/capabilityio 复活了：`capability` 是能力数据面（L1），" +
			"存量定义搬运不是它的 I/O；落点只许是 internal/drivers/transfer。")
	}

	// 不得有第二个家：同一条纪律只许在一个包里维护。
	for _, dir := range []string{
		filepath.Join(root, "internal", "drivers", "mechanism"),
		filepath.Join(root, "internal", "domain"),
	} {
		other := dirProductionBody(t, dir)
		for _, a := range append(append([]string{}, transferEntryAnchors...), transferFrameworkAnchors...) {
			if strings.Contains(other, a) {
				t.Errorf("%s 里又出现 %q：导入/导出编排只能有一个家（internal/drivers/transfer），"+
					"第二个家会让「幂等 / 绝不覆盖 / 不回写」静默分叉。", rel(root, dir), a)
			}
		}
	}
}

// TestPortAdaptersStayAtAssemblyPoint 窄端口适配体（DefinitionFiles / Secrets）只许在装配点。
//
// 它们不是「定义搬运」的一部分：一个是 automation 的文件载体端口，一个是 provider 的密钥面端口。
// 放在一起的后果不是编译错误，而是「读目录的人以为这两件事有关」——2026-10-20 之前的
// `capabilityio` 就是这样长成一个杂物间的。
func TestPortAdaptersStayAtAssemblyPoint(t *testing.T) {
	root := backendRoot(t)
	reg := dirProductionBody(t, filepath.Join(root, "internal", "drivers", "registry"))
	for _, a := range []string{
		"var _ automation.DefinitionFiles = AutomationDefinitionFiles{}",
		"var _ provider.Secrets = providerSecrets{}",
	} {
		if !strings.Contains(reg, a) {
			t.Errorf("internal/drivers/registry 里找不到 %q：窄端口适配体必须留在装配点"+
				"（只有它同时看得见域端口与机制实现；机制层禁止 import 域）。", a)
		}
	}
	tr := dirProductionBody(t, filepath.Join(root, "internal", "drivers", "transfer"))
	for _, a := range []string{"automation.DefinitionFiles", "provider.Secrets"} {
		if strings.Contains(tr, a) {
			t.Errorf("internal/drivers/transfer 里出现 %q：适配体又和导入/导出混回一个包了。", a)
		}
	}
}

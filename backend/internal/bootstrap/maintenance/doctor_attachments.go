package maintenance

import (
	"context"
	"fmt"

	doctorfacts "github.com/morehao/ark-mate/internal/drivers/mechanism/doctor"
)

// 本文件是 `mate doctor` 的**附件**体检段（v32 附件链路）。
//
// 分工同 provider 段：**事实由 `doctorfacts.CollectAttachmentDoctorFacts` 构造**（只读 SQL + stat blob），
// 本文件只做两件事——打开只读句柄、把事实翻成三档结论与人话。
//
// 退出码口径沿用既有：**"无法校验"记警告；事实坏了（悬挂/读不动）才判失败**。
//
//	attachments                行数分组：读不到 → warn（无法校验）
//	attachments 完整性         missing/unreadable > 0 → fail（附件行悬挂或 blob 不在，可重建性被破坏）
func checkAttachmentSection(opts doctorOptions) (doctorfacts.AttachmentDoctorFacts, []doctorCheck) {
	st, storeCheck := openDoctorStore(opts)
	facts := doctorfacts.CollectAttachmentDoctorFacts(context.Background(), st,
		doctorfacts.AttachmentBaseDir(opts.Home))
	if st != nil {
		defer func() { _ = st.Close() }()
	}

	var checks []doctorCheck
	if st == nil {
		checks = append(checks, storeCheck)
		return facts, checks
	}
	if facts.Note != "" {
		checks = append(checks, doctorCheck{"attachments", doctorWarn, facts.Note})
	} else {
		checks = append(checks, doctorCheck{"attachments", doctorOK,
			fmt.Sprintf("共 %d 条（pending=%d linked=%d orphan=%d）",
				facts.Total, facts.Pending, facts.Linked, facts.Orphan)})
	}
	broken := facts.Missing + facts.Unreadable
	switch {
	case facts.Note != "":
		// 已记 warn，不重复。
	case broken > 0:
		checks = append(checks, doctorCheck{"attachments 完整性", doctorFail,
			fmt.Sprintf("missing=%d unreadable=%d（附件行悬挂 / blob 缺失，可重建性被破坏）",
				facts.Missing, facts.Unreadable)})
	default:
		checks = append(checks, doctorCheck{"attachments 完整性", doctorOK,
			"全部附件行的 upload 行与磁盘 blob 均可达"})
	}
	return facts, checks
}

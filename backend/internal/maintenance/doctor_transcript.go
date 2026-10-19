package maintenance

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/object/objid"
)

// checkTranscriptShape 是 `mate doctor` 的 R3 检查：transcript 目录里有没有**旧形状**的会话 id。
//
// 为什么需要它（v15 / Q10）：平台会话 id 从裸 uuid 换成 `ses_<26 位 ULID>` 之后，旧 transcript
// 在**装载期被跳过 + WARN**（旧数据不迁移）。那条 WARN 只在启动那一刻出现一次，而用户真正会问的
// 问题是"我那些会话去哪了"——doctor 把这个事实变成随时可查的体检项，并给出**显式**处置命令
// （`mate transcripts prune`；ADR-5：不自动删，transcript 是 memory 模式的事实源）。
//
// 三档结论（沿用本命令"无法校验记警告"的既有口径）：
//   - 目录不存在 / 为空     → ok（没有 transcript 是合法状态）；
//   - 全是平台形状          → ok（报条数）；
//   - 存在旧形状            → **warn** + 打印处置命令（不是 fail：旧文件不影响新会话，
//     不清理的代价只是"那批历史会话看不见"）；
//   - 目录读不出来          → warn（"无法校验"不许静默当通过）。
func checkTranscriptShape(dir string) doctorCheck {
	const name = "transcripts"
	if strings.TrimSpace(dir) == "" {
		return doctorCheck{name, doctorOK, "未配置 transcript 目录（该形态不落盘 transcript）"}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{name, doctorOK, fmt.Sprintf("目录不存在（%s）：没有 transcript 是合法状态", dir)}
		}
		return doctorCheck{name, doctorWarn, fmt.Sprintf("无法读取 %s（%v）：**无法校验**，不当作通过", dir, err)}
	}
	var legacy []string
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		total++
		sid := strings.TrimSuffix(e.Name(), ".jsonl")
		if !objid.IsPlatformID(sid) {
			legacy = append(legacy, e.Name())
		}
	}
	if total == 0 {
		return doctorCheck{name, doctorOK, fmt.Sprintf("目录为空（%s）", dir)}
	}
	if len(legacy) == 0 {
		return doctorCheck{name, doctorOK, fmt.Sprintf("%d 份 transcript，会话 id 全为平台形状（ses_<26 位 ULID>）", total)}
	}
	sort.Strings(legacy)
	shown := legacy
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return doctorCheck{name, doctorWarn, fmt.Sprintf(
		"%d/%d 份 transcript 的会话 id 不是平台形状（v15 起装载期会**跳过**它们，旧数据不迁移）：%s%s\n"+
			"        处置（显式执行，本命令不自动删）：mate transcripts prune --older-than 1s",
		len(legacy), total, strings.Join(shown, "、"),
		map[bool]string{true: " …", false: ""}[len(legacy) > len(shown)])}
}

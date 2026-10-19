package maintenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtosystem"
)

// 本文件是 `serve --payloads` 的**解析层**（M3-4 的 Go 侧一半）。
//
// 分层理由与 `--auth-stdin` 完全相同：JSON 只在这一层被拆开，internal/app 只收**结构化值**
// （app.WithPayloads）。app 包因此不认识 wire 形状，也不做任何字符串级校验。

// payloadSourceBundled 等是壳侧三级解析（设计 §6.5：显式配置 → 随包 → PATH）的命中级别，
// 取值与 dtosystem.PayloadInfo.Source 的语义**逐字一致**（那边是取值语义的事实源）。
const (
	payloadSourceBundled    = "bundled"
	payloadSourceRuntimeEnv = "runtime-env"
	payloadSourcePath       = "path"
)

// maxPayloads 生效载荷条数上限。
//
// 为什么要有上限：这是**壳交来的诊断元数据**，不是业务数据——量级本该是个位数（每个随包
// runtime 一份）。给个防呆上限（16）是为了让"壳拼错了 JSON、把一堆无关对象塞进来"在启动期
// 就暴露，而不是让一份荒唐的清单一路带进 `/meta` 与启动日志。超限报错而不是截断：
// 截断后的清单会**看起来完整**，那比报错更坏。
const maxPayloads = 16

// payloadWire 是 `--payloads` 一条的 wire 形状。
//
// 刻意**没有** `entry` 字段：壳的清单里带它（那是壳自己解析出来的可执行入口绝对路径），
// 但 Go 侧不需要——`/meta` 的契约里没有这个字段，为了它扩契约等于把壳的内部布局
// （包内路径）写进对外响应。encoding/json 默认忽略未知字段，因此壳照发、核心不存。
type payloadWire struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Source  string `json:"source"`
}

// ParsePayloads 解析并校验桌面壳交来的**已解析**载荷清单（唯一真源是壳：只有它做了三级解析）。
//
// 校验全部发生在启动期，且一律**报错退出**（命令层转成退出码 2）——宁可起不来，也不要让
// `/meta` 展示一个假来源：诊断面一旦说谎，"机器上恰好装过 dsh"这类问题就再也查不出来。
//
// 刻意**不**校验 version / digest 非空：`path` 命中（PATH 上捡到的用户自带底座）可能根本
// 报不出可靠版本，更算不出摘要，而那正是需要如实展示"命中级别 = path"的场景。身份键是
// name（唯一性）与 source（命中级别），这两个必须有。
func ParsePayloads(raw string) ([]dtosystem.PayloadInfo, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		// 命令层用 `!= ""` 把"没传"与"传了空串"一起挡在外面；到这里说明调用方漏了门。
		return nil, errors.New("--payloads: 载荷清单为空（不传该 flag 即表示非桌面态）")
	}
	// 先挡住 null / 对象：`json.Unmarshal("null", &[]T)` 是**成功**的（得到 nil），
	// 若放它过去，`--payloads null` 会静默变成"没有载荷"，而壳的本意明显是写错了。
	if !strings.HasPrefix(s, "[") {
		return nil, fmt.Errorf("--payloads: 期望一个 JSON 数组（形如 [{\"name\":\"dsh\",\"source\":\"bundled\"}]），实际以 %q 开头", headRunes(s, 16))
	}
	var wire []payloadWire
	if err := json.Unmarshal([]byte(s), &wire); err != nil {
		return nil, fmt.Errorf("--payloads: 不是合法 JSON（%v）", err)
	}
	if len(wire) > maxPayloads {
		return nil, fmt.Errorf("--payloads: 载荷条数 %d 超过上限 %d（清单量级应为个位数；壳拼错了？）", len(wire), maxPayloads)
	}

	out := make([]dtosystem.PayloadInfo, 0, len(wire))
	// 空数组 → 非 nil 的空切片：`/meta` 恒发 `[]` 而不是 null（壳与前端只写一种分支）。
	seen := make(map[string]bool, len(wire))
	for i, w := range wire {
		name := strings.TrimSpace(w.Name)
		if name == "" {
			return nil, fmt.Errorf("--payloads: 第 %d 条缺 name（name 是载荷身份，缺了就无法在诊断面区分是哪一份）", i+1)
		}
		source := strings.TrimSpace(w.Source)
		switch source {
		case payloadSourceBundled, payloadSourceRuntimeEnv, payloadSourcePath:
		case "":
			return nil, fmt.Errorf("--payloads: 第 %d 条（%s）缺 source（命中级别必须由壳显式给出，核心不猜）", i+1, name)
		default:
			return nil, fmt.Errorf("--payloads: 第 %d 条（%s）的 source=%q 非法，只接受 %s|%s|%s",
				i+1, name, w.Source, payloadSourceBundled, payloadSourceRuntimeEnv, payloadSourcePath)
		}
		if seen[name] {
			// 同名两条会让"命中了哪一份"无法判定（诊断面读到的取值取决于顺序），必须报错。
			return nil, fmt.Errorf("--payloads: name %q 重复（同一份载荷只能出现一次，否则来源不可判定）", name)
		}
		seen[name] = true
		out = append(out, dtosystem.PayloadInfo{
			Name:    name,
			Version: strings.TrimSpace(w.Version),
			Digest:  strings.TrimSpace(w.Digest),
			Source:  source,
		})
	}
	return out, nil
}

// headRunes 取前 n 个字符，仅用于把非法输入摘进错误文案（多字节字符按 rune 截，不切坏）。
func headRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

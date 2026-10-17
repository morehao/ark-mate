package runtimeconn

import (
	"strconv"
	"strings"
)

// ——— 连接键与代际键的**唯一**实现 ———
//
// 这两个键决定"哪些会话共享一个暖进程"，是执行面最要紧的一条判据：
//
//	connKey       —— 基础键：runtime ID + 各 env 组签名（影响线程行为的配置相同 → 共享暖进程）
//	generationKey —— 在基础键上追加**代际维度**：能力指纹 + 平台模型配置指纹
//
// 它们必须是**纯函数且只有一份**：两处各算一遍的后果不是报错，而是"迁移判据说不用换进程、
// 而 acquire 换了"这种**静默分叉**——表现为改了能力选择之后一部分会话仍跑在旧配置的进程上。
// 判据里刻意没有锁、没有 I/O、没有 Orchestrator 的字段。

// ConnKey 计算**基础**连接缓存键：runtime ID + 各 env 组签名（每组内按序 join、组间 \x00 分隔）。
// 同 runtime 且影响线程行为的 env 组合相同 → 共享暖进程；任一不同 → 各自独立进程。
// 传入组约定：nativeEnv（原生权限开关）、keyEnv（provider 密钥）、modelEnv（仅进程级模型）。
//
// **这不是代际键**：代际键 = 本键 + 能力指纹，见 GenerationKey。保留本函数形状原样是因为
// "配置相同则共享暖进程"这条既有规则要**逐字**保留（R11）。
func ConnKey(runtimeID string, envs ...[]string) string {
	var parts []string
	for _, e := range envs {
		if len(e) == 0 {
			continue
		}
		parts = append(parts, strings.Join(e, "\x00"))
	}
	if len(parts) == 0 {
		return runtimeID
	}
	return runtimeID + "\x00" + strings.Join(parts, "\x00")
}

// GenerationKey 在基础连接键之上追加**代际维度**：能力指纹 + 平台生成的模型配置指纹（T5）。
//
// 两个维度缺一不可，而且它们**必须都在键里**：
//   - 指纹相同 = 同一套能力 = 复用同一个暖进程；指纹不同 = 各起一个进程（能力集与投影根都是
//     进程级的，混用等于把 A 会话的 skill 投给 B 会话）；
//   - 配置指纹（T5）：配置文件是**进程启动时**读的。不把它并进键，改完清单文件已经变了、暖进程
//     还活着，底座继续用旧清单，而界面显示"已保存"——那是本方案最坏的一种表现。
//
// **两者都为空 → 原样返回基础键**：这是 R11（缺省零回归）的落点——既未声明能力选择、
// 该 runtime 又没有平台模型清单的会话，其键必须与代际机制之前**逐字相同**。
//
// 非空时用**带长度前缀**的形态而不是简单拼后缀：`ConnKey("r", envA)` 是
// `ConnKey("r", envA, envB)` 的字符串前缀（env 组之间就是 `\x00` 分隔），拼后缀会让"某个基础键
// 恰好是另一个基础键的前缀"与"代际后缀"互相串味。长度前缀让基础键自我定界。
func GenerationKey(connKey, fingerprint, configDigest string) string {
	if fingerprint == "" && configDigest == "" {
		return connKey
	}
	return "gen:" + strconv.Itoa(len(connKey)) + ":" + connKey + ":" + fingerprint + ":" + configDigest
}

// ShortGenerationKeys 把一串代际键压成人看的短标签（诊断/日志用；只留最后一段的前 8 字符）。
func ShortGenerationKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if i := strings.LastIndex(k, ":"); i >= 0 && i+1 < len(k) {
			k = k[i+1:]
		}
		if k == "" {
			k = "缺省"
		} else if len(k) > 8 {
			k = k[:8]
		}
		out = append(out, k)
	}
	return out
}

// ConnCauseLabel 连接拆除原因的标签（`intentional` = 我方主动关：daemon 停机 / 空闲回收）。
func ConnCauseLabel(intentional bool) string {
	if intentional {
		return "intentional"
	}
	return "unexpected"
}

// DedupeStrings 保序去重（连接键的 env 组拼装与诊断都用；纯函数）。
func DedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

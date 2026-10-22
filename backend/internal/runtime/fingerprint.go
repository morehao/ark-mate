package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sort"
	"strconv"
)

// ——— 能力指纹（§13.4 代际；ADR-7/ADR-8 的内容寻址键） ———
//
// 指纹是「这一套能力」的**内容摘要**：勾选结果 → 指纹 → 内容寻址的代际（进程 + 投影根）。
// 指纹相同 = 同一套能力 = 可以复用同一个暖进程；指纹不同 = 必须各起一个进程
// （能力集与投影根都是进程级的，混用等于把 A 会话的 skill 投给 B 会话）。
//
// **输入缺一不可**（漏一项就会让两套不同的东西共用一个代际）：
//
//  1. 该 runtime 下**解析后**的 skill 列表：id + 正文摘要；
//  2. 该 runtime 下**解析后**的 MCP 定义：id + transport + command/args/url + env **键名**
//     （密钥只记 `store:`/`env:` **引用名**，**绝不记密钥值**——指纹会进日志/事件/DB 列，
//     记了值就等于把密钥泄漏出去）；
//  3. 投影形态：`Projection{Kind,Scope,Root,Layout,Format}`；
//  4. 底座版本（见 `RuntimePart.Version` 的边界说明）。
//
// **稳定性是硬要求**：Go 的 map 遍历顺序随机，任何一处直接遍历 map 都会让同一个能力集每次算出
// 不同指纹，后果是"每次建会话都新建代际、进程数失控"（风险表第 15 行）。所以这里只接受**切片**，
// 且在序列化前统一排序；调用方负责把 map 展开成切片。
//
// **包边界**：本文件不 import `pkg/capability`——skill 的正文摘要由调用方用 sidecar 的同一算法
// 算好（`capability.PayloadHash`）后传进来。摘要算法因此只有一份实现，而投影层不必把整个能力
// 数据面拖进来。

// FingerprintInput 参与一次指纹计算的全部输入。
//
// 三个切片的**元素顺序不影响结果**（内部会排序）；重复项（同 ID 出现两次）会被去重——
// 重复本身就是"同一份定义被装载了两次"，不该产生第二套代际。
type FingerprintInput struct {
	Skills   []SkillPart
	Mcps     []McpPart
	Runtimes []RuntimePart
}

// SkillPart 一条**解析后**的 skill（已确定它会投放进本次代际）。
type SkillPart struct {
	ID string
	// BodyDigest 正文内容摘要。由调用方用**物化 sidecar 的同一算法**算出
	// （`capability.PayloadHash(body)`）——本包不重复实现摘要算法。
	BodyDigest string
}

// McpPart 一条**解析后**的 MCP 定义。
//
// 注意这里**没有**任何可以放密钥值的字段：env 只有键名，密钥只有引用名。
// "值不进指纹"因此是**结构性**的，而不是靠调用方记得剔除。
type McpPart struct {
	ID string
	// Transport stdio | sse | http | streamable_http。
	Transport string
	// Command/Args/URL 启动形态（进程怎么用、连哪里，都属于能力集的一部分）。
	Command string
	Args    []string
	URL     string
	// EnvKeys 明文 env 的**键名**（值不进指纹：值变了底座行为会变，但那是"同一套能力的另一个
	// 运行参数"，而且值可能带凭据）。
	EnvKeys []string
	// SecretRefs 密钥型 env/header 的**引用名**（`store:<ns>/<name>` / `env:<NAME>`），
	// **不是**解析后的值。改引用名（换一把钥匙）要产生新代际，改引用的**指向**（轮换密钥）
	// 不产生——轮换密钥不该让所有会话换底座进程。
	SecretRefs []string
}

// RuntimePart 参与指纹的一个 runtime 的形态。
//
// 调用方决定纳入哪些 runtime：M1-10 的 `connKey` 只纳入**当前这一个**——一个底座的投影形态变了
// 不该让别的底座的代际跟着换进程。
type RuntimePart struct {
	ID string
	// Version 底座版本声明（`Spec.Version`）。
	//
	// **已知边界（如实记录，不假装）**：这是 adapter **声明**的版本串，不是 ACP 握手**观测**到的
	// 底座版本——握手只在建连之后发生，而指纹必须在那之前算出来（它就是用来决定复用哪个连接的）。
	// 所以底座升级要产生新代际，得由 adapter 在升级适配时同步改声明
	// （`internal/infra/<adapter>/adapter.go` 里 `Version: <adapter 包>.<常量>` 这种写法）。P-2 顺带记录 `initialize` 能否读到真实版本，
	// 若能，M3 之后可以换成观测值（那是"指纹更准"，不是"指纹不对"）。
	Version string
	// Scope 能力面的绑定粒度（CapabilityScope；空 = process）。
	Scope string
	// Projections 原生形态声明（Kind/Scope/Root/Layout/Format）。
	Projections []Projection
}

// Fingerprint 计算能力指纹（sha256 十六进制小写）。
//
// **输入为空**（三个切片都空）时返回 `""` 而不是一个"空能力集"的摘要：空指纹的含义是
// "本次会话没有声明能力选择"，M1-10 的 `connKey` 据此保持与今天**逐字相同**的键
// （R11 缺省零回归——同配置的两个会话仍共享同一个暖进程）。
func Fingerprint(in FingerprintInput) string {
	if len(in.Skills) == 0 && len(in.Mcps) == 0 && len(in.Runtimes) == 0 {
		return ""
	}
	h := sha256.New()
	writeFramed(h, "ark-mate/capability-fingerprint/v1")

	writeFramed(h, "skills")
	for _, s := range sortSkills(in.Skills) {
		writeFramed(h, "skill")
		writeFramed(h, s.ID)
		writeFramed(h, s.BodyDigest)
	}

	writeFramed(h, "mcps")
	for _, m := range sortMcps(in.Mcps) {
		writeFramed(h, "mcp")
		writeFramed(h, m.ID)
		writeFramed(h, m.Transport)
		writeFramed(h, m.Command)
		writeFramed(h, "args")
		for _, a := range m.Args {
			writeFramed(h, a)
		}
		writeFramed(h, m.URL)
		writeFramed(h, "env-keys")
		for _, k := range sortedCopy(m.EnvKeys) {
			writeFramed(h, k)
		}
		writeFramed(h, "secret-refs")
		for _, r := range sortedCopy(m.SecretRefs) {
			writeFramed(h, r)
		}
	}

	writeFramed(h, "runtimes")
	for _, r := range sortRuntimes(in.Runtimes) {
		writeFramed(h, "runtime")
		writeFramed(h, r.ID)
		writeFramed(h, r.Version)
		writeFramed(h, r.Scope)
		writeFramed(h, "projections")
		for _, p := range sortedProjections(r.Projections) {
			writeFramed(h, "projection")
			writeFramed(h, string(p.Kind))
			writeFramed(h, p.Scope)
			writeFramed(h, p.Root)
			writeFramed(h, p.Layout)
			writeFramed(h, p.Format)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeFramed 写一段**带长度前缀**的字段。
//
// 为什么不是 `fmt.Fprintf(h, "%s|%s\n", a, b)`：那样 `("a|b", "c")` 与 `("a", "b|c")` 会哈希成
// 同一个值——分隔符出现在值里就能伪造出碰撞。长度前缀让每一段自定界，拼接方式无法歧义。
func writeFramed(h hash.Hash, s string) {
	_, _ = h.Write([]byte(strconv.Itoa(len(s))))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(s))
	_, _ = h.Write([]byte{'\n'})
}

// sortedCopy 排序副本（不改调用方的切片——调用方可能持有它做别的事）。
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// sortSkills 按 (id, 正文摘要) 排序。
//
// 只排序**不去重**：同一个 id 出现两次（内容相同或不同）就让它在哈希里出现两次——指纹只需要
// 「同一套定义 → 同一个值」，不需要替调用方清理输入。反过来，如果这里悄悄去重，"输入里多了一份
// 定义"与"没多"会算出同一个指纹，那是把一个真实差异抹平。
func sortSkills(in []SkillPart) []SkillPart {
	out := append([]SkillPart(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].BodyDigest < out[j].BodyDigest
	})
	return out
}

// sortMcps 按 id 排序，并把 **map 派生的**切片（EnvKeys/SecretRefs）规范化。
//
// `Args` **保持原序**（不排序）：命令行参数顺序是语义的一部分，`-y pkg` 与 `pkg -y` 不是同一条
// 命令。而 EnvKeys/SecretRefs 由调用方从 map 展开，遍历顺序随机——不排序就会让同一套定义每次
// 算出不同指纹（这正是风险表第 15 行说的那个坑）。
func sortMcps(in []McpPart) []McpPart {
	out := append([]McpPart(nil), in...)
	for i := range out {
		out[i].EnvKeys = sortedCopy(out[i].EnvKeys)
		out[i].SecretRefs = sortedCopy(out[i].SecretRefs)
		out[i].Args = append([]string(nil), out[i].Args...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sortRuntimes 按 id 排序，并规范化投影列表。
func sortRuntimes(in []RuntimePart) []RuntimePart {
	out := append([]RuntimePart(nil), in...)
	for i := range out {
		out[i].Projections = sortedProjections(out[i].Projections)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// projectionKey 投影形态的规范化比较键（**只含参与指纹的五个字段**）。
//
// `Tested` / `Note` 刻意不参与：它们描述的是"我们对这个形态的把握程度"，不是形态本身。
// P-2 实测完把 `Tested` 置 true 不该让所有会话换一次底座进程。
func projectionKey(p Projection) string {
	return string(p.Kind) + "\x00" + p.Scope + "\x00" + p.Root + "\x00" + p.Layout + "\x00" + p.Format
}

// sortedProjections 投影形态按规范化键排序去重。
func sortedProjections(in []Projection) []Projection {
	out := append([]Projection(nil), in...)
	sort.Slice(out, func(i, j int) bool { return projectionKey(out[i]) < projectionKey(out[j]) })
	dedup := out[:0]
	for i, p := range out {
		if i > 0 && projectionKey(p) == projectionKey(out[i-1]) {
			continue
		}
		dedup = append(dedup, p)
	}
	return dedup
}

package app

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
)

// ReadyPrefix 就绪行的行首前缀（M1-2）。
//
// 契约（设计 §7.1）：核心 listen 成功后往 **stdout** 打**一行**，壳按**行首**匹配它；
// 日志与它就绪行混在一起时，只有行首命中才算（前缀出现在别的日志中间不算匹配）。
// 行内**不含任何令牌**——令牌只经 stdin 交付（M1-1）。
const ReadyPrefix = "MATE_READY "

// ReadyPayload 就绪行载荷。
//
// 字段是**增量**的：M1-2 只保证 `addr` / `port`（壳启动链唯一必需的两项）；
// build id 由 M2-4 接上真实值后出现在同一行里——壳对缺失字段按"未知"处理，
// 因此现在不填不会造成"半个契约"（填一个编造的值才会）。
//
// 2026-10-10 起删掉了 `webSource`：本字段从未被填过（声明" M2-3 起填"，一直没接），而资产来源
// 又只剩"调用方给的目录"一种，留着只会让人以为它在报什么。
type ReadyPayload struct {
	Addr string `json:"addr"`
	Port int    `json:"port"`
	// WebBuildID 前端构建 id，M2-4 起填。
	WebBuildID string `json:"webBuildId,omitempty"`
}

// ReadyLine 由实际监听地址组装就绪行（形如 `MATE_READY {"addr":"127.0.0.1:51234","port":51234}`）。
//
// 地址解析失败（理论上不会：地址来自 net.Listener）时 port 记为 0 并在调用处告警——
// 宁可让壳靠 health 轮询兜底，也不要在这里 panic 掉一个已经起来的服务。
func ReadyLine(addr string) (string, error) {
	p := ReadyPayload{Addr: addr}
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		if port, convErr := strconv.Atoi(portStr); convErr == nil {
			p.Port = port
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("组装就绪行失败: %w", err)
	}
	return ReadyPrefix + string(b), nil
}

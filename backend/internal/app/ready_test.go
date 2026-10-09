package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestReadyLineShape 就绪行的形状是壳的解析契约（M1-2）：行首前缀 + 一行 JSON + **不含令牌**。
func TestReadyLineShape(t *testing.T) {
	line, err := ReadyLine("127.0.0.1:51234")
	if err != nil {
		t.Fatalf("ReadyLine: %v", err)
	}
	if !strings.HasPrefix(line, ReadyPrefix) {
		t.Fatalf("就绪行必须以 %q 开头（壳按行首匹配）：%q", ReadyPrefix, line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("就绪行必须是一行：%q", line)
	}
	// 令牌绝不能出现在就绪行里（M1-1 的令牌只经 stdin）。
	for _, banned := range []string{"token", "Token", "secret", "Authorization"} {
		if strings.Contains(line, banned) {
			t.Fatalf("就绪行不得含凭据相关字样 %q：%s", banned, line)
		}
	}

	var p ReadyPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, ReadyPrefix)), &p); err != nil {
		t.Fatalf("就绪行载荷不是合法 JSON：%v（%s）", err, line)
	}
	if p.Addr != "127.0.0.1:51234" || p.Port != 51234 {
		t.Fatalf("载荷不符：%+v", p)
	}
	// M2-3 之前这两个字段缺席（壳按未知处理）——出现即说明有人填了编造的值。
	if p.WebSource != "" || p.WebBuildID != "" {
		t.Fatalf("M1-2 阶段不应填资产字段（M2-3 才接真值）：%+v", p)
	}
}

// TestReadyLineIPv6 地址解析对 IPv6 字面量同样成立（Host 白名单里 [::1] 是一等公民）。
func TestReadyLineIPv6(t *testing.T) {
	line, err := ReadyLine("[::1]:18001")
	if err != nil {
		t.Fatalf("ReadyLine: %v", err)
	}
	var p ReadyPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, ReadyPrefix)), &p); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if p.Addr != "[::1]:18001" || p.Port != 18001 {
		t.Fatalf("IPv6 载荷不符：%+v", p)
	}
}

// TestReadyLineUnparsableAddr 异常地址不 panic：port 记 0（壳会退回 health 轮询）。
func TestReadyLineUnparsableAddr(t *testing.T) {
	line, err := ReadyLine("不合法地址")
	if err != nil {
		t.Fatalf("不应报错（宁可让壳靠轮询兜底）：%v", err)
	}
	var p ReadyPayload
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, ReadyPrefix)), &p); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if p.Port != 0 || p.Addr != "不合法地址" {
		t.Fatalf("异常地址应如实回写 addr、port=0：%+v", p)
	}
}

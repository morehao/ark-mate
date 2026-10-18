package skill

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// ——— 技能包解析（D1）的单测 ———
//
// 这一组钉的是"包被拒在哪一条上"：zip slip、符号链接、额外文件、条目数、压缩比、front matter
// 缺键。它们全都是**写面之外的输入**，任何一条漏掉都不会让写面报错——只会让一份不该被接受的包
// 变成一条可用的技能定义。

type zipEntry struct {
	name string
	body string
	mode fs.FileMode
}

func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("zip create %s: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatalf("zip write %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func parseZip(t *testing.T, entries []zipEntry) (*Package, error) {
	t.Helper()
	b := buildZip(t, entries)
	return ParsePackage(bytes.NewReader(b), int64(len(b)))
}

const validSKILLMD = `---
name: weekly-report
description: 从本周会话、提交与文档变更中提炼进展
title: 周报生成器
---

# 周报生成器

## 何时使用
用户要求「写周报」时。
`

func TestParsePackageRootSkillMD(t *testing.T) {
	pkg, err := parseZip(t, []zipEntry{{name: "SKILL.md", body: validSKILLMD}})
	if err != nil {
		t.Fatalf("合法包被拒: %v", err)
	}
	if pkg.ID != "weekly-report" {
		t.Fatalf("id = %q，期望 weekly-report", pkg.ID)
	}
	if pkg.Name != "周报生成器" {
		t.Fatalf("展示名应取 title，got %q", pkg.Name)
	}
	if !strings.Contains(pkg.Description, "本周会话") {
		t.Fatalf("description = %q", pkg.Description)
	}
	if !strings.HasPrefix(pkg.Body, "---\nname: weekly-report") {
		t.Fatalf("正文应保留完整 SKILL.md（含 front matter）: %q", pkg.Body[:20])
	}
}

func TestParsePackageWrapperDirAndMetadata(t *testing.T) {
	// 唯一一层包装目录 + Finder 生成的元数据：元数据忽略，不因它拒绝整个包。
	pkg, err := parseZip(t, []zipEntry{
		// `zip -r` 会写目录条目（以 `/` 结尾）：它们是结构，不是"额外文件"。
		{name: "weekly-report/", body: "", mode: fs.ModeDir | 0o755},
		{name: "weekly-report/SKILL.md", body: validSKILLMD},
		{name: "__MACOSX/", body: "", mode: fs.ModeDir | 0o755},
		{name: "__MACOSX/._SKILL.md", body: "x"},
		{name: ".DS_Store", body: "x"},
	})
	if err != nil {
		t.Fatalf("带包装目录/元数据的合法包被拒: %v", err)
	}
	if pkg.ID != "weekly-report" {
		t.Fatalf("id = %q", pkg.ID)
	}
}

func TestParsePackageRejectsBadShapes(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
		want    string
	}{
		{
			name:    "缺 front matter",
			entries: []zipEntry{{name: "SKILL.md", body: "# 没有 front matter\n"}},
			want:    "front matter",
		},
		{
			name:    "缺 name",
			entries: []zipEntry{{name: "SKILL.md", body: "---\ndescription: 只有描述\n---\n正文"}},
			want:    "缺少 name",
		},
		{
			name:    "缺 description",
			entries: []zipEntry{{name: "SKILL.md", body: "---\nname: demo\n---\n正文"}},
			want:    "缺少 description",
		},
		{
			name: "额外文件",
			entries: []zipEntry{
				{name: "SKILL.md", body: validSKILLMD},
				{name: "run.sh", body: "#!/bin/sh"},
			},
			want: "只允许 SKILL.md",
		},
		{
			name:    "zip slip",
			entries: []zipEntry{{name: "pkg/../../SKILL.md", body: validSKILLMD}},
			want:    "越界",
		},
		{
			name:    "绝对路径",
			entries: []zipEntry{{name: "/SKILL.md", body: validSKILLMD}},
			want:    "绝对路径",
		},
		{
			name: "多个 SKILL.md",
			entries: []zipEntry{
				{name: "SKILL.md", body: validSKILLMD},
				{name: "pkg/SKILL.md", body: validSKILLMD},
			},
			want: "多个 SKILL.md",
		},
		{
			name:    "两层目录",
			entries: []zipEntry{{name: "a/b/SKILL.md", body: validSKILLMD}},
			want:    "唯一一层目录",
		},
		{
			name:    "符号链接",
			entries: []zipEntry{{name: "SKILL.md", body: "/etc/passwd", mode: os.ModeSymlink | 0o777}},
			want:    "符号链接",
		},
		{
			name:    "中文 name 无法规范化",
			entries: []zipEntry{{name: "SKILL.md", body: "---\nname: 周报生成器\ndescription: 中文\n---\n正文"}},
			want:    "无法规范化",
		},
		{
			name:    "NUL 字节",
			entries: []zipEntry{{name: "SKILL.md", body: "---\nname: demo\ndescription: x\n---\n正文\x00"}},
			want:    "NUL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseZip(t, tc.entries)
			if err == nil {
				t.Fatalf("应被拒，但解析成功")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误应含 %q，got: %v", tc.want, err)
			}
		})
	}
}

func TestParsePackageEntryLimit(t *testing.T) {
	entries := make([]zipEntry, 0, maxPackageEntries+1)
	entries = append(entries, zipEntry{name: "SKILL.md", body: validSKILLMD})
	for i := 0; i < maxPackageEntries; i++ {
		entries = append(entries, zipEntry{name: ".DS_Store", body: "x"})
	}
	_, err := parseZip(t, entries)
	if err == nil || !strings.Contains(err.Error(), "条目数") {
		t.Fatalf("超过条目上限应被拒，got: %v", err)
	}
}

func TestParsePackageRejectsCompressionBomb(t *testing.T) {
	// 2 MiB 的同字符内容压缩比远超 100：在解析 front matter 之前就该被拒。
	body := strings.Repeat("A", 2<<20)
	_, err := parseZip(t, []zipEntry{{name: "SKILL.md", body: body}})
	if err == nil || !strings.Contains(err.Error(), "压缩比异常") {
		t.Fatalf("高压缩比应被拒，got: %v", err)
	}
}

func TestParsePackageNormalizesNameAndCRLF(t *testing.T) {
	pkg, err := parseZip(t, []zipEntry{{
		name: "SKILL.md",
		body: "---\r\nname: Weekly_Report.v2\r\ndescription: 周报\r\n---\r\n\r\n# 正文\r\n",
	}})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if pkg.ID != "weekly-report-v2" {
		t.Fatalf("id 规范化 = %q，期望 weekly-report-v2", pkg.ID)
	}
	if !strings.HasPrefix(pkg.Body, "---\nname: Weekly_Report.v2\n") {
		t.Fatalf("CRLF 应折成 LF: %q", pkg.Body[:24])
	}
}

func TestNormalizePackageID(t *testing.T) {
	cases := map[string]string{
		"weekly-report":    "weekly-report",
		"Weekly Report":    "weekly-report",
		"  PDF__toolkit  ": "pdf-toolkit",
		"周报生成器":            "",
		"周报 v2":            "v2",
		"--a--b--":         "a-b",
		"a.b_c":            "a-b-c",
	}
	for in, want := range cases {
		if got := normalizePackageID(in); got != want {
			t.Fatalf("normalizePackageID(%q) = %q，期望 %q", in, got, want)
		}
	}
}

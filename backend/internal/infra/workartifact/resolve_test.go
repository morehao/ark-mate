package workartifact

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/artifact"
)

// TestResolveToolWrite_WritesAndEdits 是 R1 探针记录的真实形状（write/edit 两种）的正例。
func TestResolveToolWrite_WritesAndEdits(t *testing.T) {
	cases := []struct {
		name       string
		info       protocol.ToolCallInfo
		wantOK     bool
		wantRel    string
		wantOrigin artifact.Origin
	}{
		{
			name: "write 带 file_path+content（R1 实测形状）",
			info: protocol.ToolCallInfo{ID: "c1", Title: "write", Status: "completed",
				Input: map[string]any{"file_path": "r1_probe.txt", "content": "alpha\nbeta\n"}},
			wantOK: true, wantRel: "r1_probe.txt", wantOrigin: artifact.OriginToolWrite,
		},
		{
			name: "edit 带 file_path+old/new_string（R1 实测形状）",
			info: protocol.ToolCallInfo{ID: "c2", Title: "edit", Status: "completed",
				Input: map[string]any{"file_path": "sub/dir/a.go", "old_string": "x", "new_string": "y"}},
			wantOK: true, wantRel: "sub/dir/a.go", wantOrigin: artifact.OriginToolEdit,
		},
		{
			name: "相对路径带 ../ 归一（Clean，不在这里判越界）",
			info: protocol.ToolCallInfo{ID: "c3", Title: "write",
				Input: map[string]any{"file_path": "./a/../b.txt"}},
			wantOK: true, wantRel: "b.txt", wantOrigin: artifact.OriginToolWrite,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ResolveToolWrite(c.info)
			if ok != c.wantOK {
				t.Fatalf("ok=%v 期望 %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if got.RelPath != c.wantRel {
				t.Errorf("RelPath=%q 期望 %q", got.RelPath, c.wantRel)
			}
			if got.Origin != c.wantOrigin {
				t.Errorf("Origin=%q 期望 %q", got.Origin, c.wantOrigin)
			}
		})
	}
}

// TestResolveToolWrite_NotExtractable 覆盖「不该抽」的所有形状：非白名单 title、缺 file_path、
// 形状不符、绝对路径。
func TestResolveToolWrite_NotExtractable(t *testing.T) {
	cases := []struct {
		name string
		info protocol.ToolCallInfo
	}{
		{"title=read（非落文件）", protocol.ToolCallInfo{Title: "read", Input: map[string]any{"file_path": "a"}}},
		{"title=bash", protocol.ToolCallInfo{Title: "bash", Input: map[string]any{"command": "ls"}}},
		{"title 空", protocol.ToolCallInfo{Title: "", Input: map[string]any{"file_path": "a"}}},
		{"title=write 但 rawInput 无 file_path", protocol.ToolCallInfo{Title: "write", Input: map[string]any{"content": "x"}}},
		{"title=write 但 Input 为 nil", protocol.ToolCallInfo{Title: "write", Input: nil}},
		{"title=write 但 Input 非 map（形状不符）", protocol.ToolCallInfo{Title: "write", Input: "raw-string"}},
		{"file_path 非字符串", protocol.ToolCallInfo{Title: "write", Input: map[string]any{"file_path": 123}}},
		{"绝对路径（非工作区产物常态，保守跳过）", protocol.ToolCallInfo{Title: "write", Input: map[string]any{"file_path": "/etc/passwd"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := ResolveToolWrite(c.info); ok {
				t.Fatalf("不应抽取，却得到 %+v", got)
			}
		})
	}
}

// TestSafeRelPath 覆盖路径安全解析：正常相对、越界 `../`、符号链接逃逸、cwd 非绝对、非相对文件。
func TestSafeRelPath(t *testing.T) {
	cwd := t.TempDir() // 绝对路径
	// 造一个 cwd 内的真实文件与一个指向 cwd 外的符号链接，验证真身解析能挡逃逸。
	if err := os.WriteFile(filepath.Join(cwd, "keep.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkEscape := filepath.Join(cwd, "escape-link")
	_ = os.Symlink(outFile, linkEscape) // 指向 cwd 外

	t.Run("正常相对路径", func(t *testing.T) {
		abs, rel, err := SafeRelPath(cwd, "keep.txt")
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if abs != filepath.Join(cwd, "keep.txt") {
			t.Errorf("abs=%q", abs)
		}
		if rel != "keep.txt" {
			t.Errorf("rel=%q", rel)
		}
	})

	t.Run("子目录相对路径 slanch 化", func(t *testing.T) {
		_, rel, err := SafeRelPath(cwd, "sub/dir/a.go")
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if rel != "sub/dir/a.go" {
			t.Errorf("rel=%q", rel)
		}
	})

	t.Run("越界 ../ 被拒", func(t *testing.T) {
		_, _, err := SafeRelPath(cwd, "../outside.txt")
		assertPathReason(t, err, "path_escape")
	})

	t.Run("多层越界 ../../ 被拒", func(t *testing.T) {
		_, _, err := SafeRelPath(cwd, "../../a/b.txt")
		assertPathReason(t, err, "path_escape")
	})

	t.Run("符号链接逃逸被拒", func(t *testing.T) {
		_, _, err := SafeRelPath(cwd, "escape-link")
		// 逃逸的目标在 cwd 外 → path_escape（WithinRoots 做真身解析）
		assertPathReason(t, err, "path_escape")
	})

	t.Run("cwd 非绝对被拒", func(t *testing.T) {
		_, _, err := SafeRelPath("relative/cwd", "a.txt")
		assertPathReason(t, err, "cwd_not_absolute")
	})

	t.Run("绝对路径被拒（应传相对路径）", func(t *testing.T) {
		_, _, err := SafeRelPath(cwd, "/etc/passwd")
		assertPathReason(t, err, "path_not_relative_file")
	})

	t.Run("当前目录 . 被拒（不是文件）", func(t *testing.T) {
		_, _, err := SafeRelPath(cwd, ".")
		assertPathReason(t, err, "path_not_relative_file")
	})
}

func assertPathReason(t *testing.T, err error, want string) {
	t.Helper()
	var pe *PathError
	if !errors.As(err, &pe) {
		t.Fatalf("期望 *PathError，得到 %v", err)
	}
	if pe.Reason != want {
		t.Fatalf("Reason=%q 期望 %q", pe.Reason, want)
	}
}

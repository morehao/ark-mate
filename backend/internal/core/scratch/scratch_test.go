package scratch

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestPathFor(t *testing.T) {
	cases := []struct {
		name, cwd, sid, want string
	}{
		{"normal", "/ws/a", "ses_01", filepath.Join("/ws/a", ".scratch", "ses_01")},
		{"empty cwd", "", "ses_01", ""},
		{"empty sid", "/ws/a", "", ""},
		{"sid with slash", "/ws/a", "a/b", ""},
		{"sid dotdot", "/ws/a", "..", ""},
		{"sid dot", "/ws/a", ".", ""},
		{"trim", "  /ws/a  ", "  ses_01  ", filepath.Join("/ws/a", ".scratch", "ses_01")},
	}
	for _, c := range cases {
		if got := PathFor(c.cwd, c.sid); got != c.want {
			t.Errorf("%s: PathFor(%q,%q)=%q, want %q", c.name, c.cwd, c.sid, got, c.want)
		}
	}
}

func TestRootFor(t *testing.T) {
	if got := RootFor("/ws/a"); got != filepath.Join("/ws/a", ".scratch") {
		t.Errorf("RootFor=/q %q", got)
	}
	if got := RootFor("  "); got != "" {
		t.Errorf("RootFor(blank)=%q, want empty", got)
	}
}

func TestIsScratchRelPath(t *testing.T) {
	cases := map[string]bool{
		".scratch/x.py":            true,
		".scratch/ses_01/a/b.docx": true,
		".scratch":                 true, // 首段就是它
		`scratch\x`:                false,
		`bash\.scratch\x`:          false,
		"build/x.py":               false,
		".scratchx/y":              false,
		"x/.scratch/y":             false, // 不在首段（正常成品不该是这样）
		"":                         false,
		".":                        false,
	}
	for in, want := range cases {
		if got := IsScratchRelPath(in); got != want {
			t.Errorf("IsScratchRelPath(%q)=%v, want %v", in, got, want)
		}
	}
}

func TestIsScratchRelPathWindowsSeparators(t *testing.T) {
	// 反斜杠分隔也要识别为首段 .scratch。
	if !IsScratchRelPath(`.scratch\ses_01\x.py`) {
		t.Errorf("windows-style scratch path should be recognized")
	}
}

func TestOrphanCandidates(t *testing.T) {
	live := map[string]bool{"ses_live": true}
	isLive := func(id string) bool { return live[id] }
	got := OrphanCandidates([]string{"ses_live", "ses_dead", "", "..", "a/b", "ses_dead2"}, isLive)
	want := []string{"ses_dead", "ses_dead2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OrphanCandidates=%v, want %v", got, want)
	}
	// nil isLive：全部视为孤儿（调用方语义：无可判依据时保守——调用方应确保此时不真删；此处只测纯筛选）。
	got2 := OrphanCandidates([]string{"a", "b"}, nil)
	if !reflect.DeepEqual(got2, []string{"a", "b"}) {
		t.Fatalf("nil isLive OrphanCandidates=%v", got2)
	}
}

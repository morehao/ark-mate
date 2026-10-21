package archtest

// 本文件钉住**会话读面自己取锁**（2026-10-17，修 `SessionResp` 的裸读 map）。
//
// 已发生的竞态：`domain/session/readface.go` 的 `SessionResp` 注释写着「须持锁调用」，但它是
// **导出**方法、锁在包里——`app/session` 的两个调用点（`detail.go` / `meta.go`）都不可能
// 持锁，于是并发 `RegisterSession` 时对 `pl.sessions` 的裸读是真实的数据竞争（`-race` 可复现）。
//
// 修法不是"给调用方也加一把锁"（那把锁在包里、跨包本来就不该拿），而是把**调用方持锁**这个
// 约定改成**方法自己取锁**：`SessionResp` 收成不导出的 `sessionRespLocked`（只给包内持锁调用），
// 跨包读面走新增的 `SessionSnapshot`（一次取锁返回 resp + run + 存在性）。
//
// 判据四段（fail-closed）：
//
//	① `readface.go` 不得再出现导出的 `func (pl *Plane) SessionResp(`；
//	② `SessionSnapshot` 必须存在，且函数体里有 `pl.mu.Lock()`；
//	③ `app/session` 的生产文件里 `SessionSnapshot(` 至少一处；
//	④ 同一扫描面 `.SessionResp(` 零命中。
//
// 反证（实跑）：把 `SessionResp` 加回导出（能编译）→ ① 红；`detail.go` 改回
// `d.plane.SessionResp(id)` → ④ 红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionReadFaceTakesOwnLock 见文件头。
func TestSessionReadFaceTakesOwnLock(t *testing.T) {
	root := backendRoot(t)
	read := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("读 %s 失败: %v（判据失去对象）", rel, err)
		}
		return string(raw)
	}

	src := stripGoComments(read("internal/domain/session/readface.go"))
	if strings.Contains(src, "func (pl *Plane) SessionResp(") {
		t.Errorf("readface.go 又导出了 SessionResp——跨包调用方拿不到包内锁，裸读 map 的竞态会回来")
	}
	body, ok := goFuncBody(src, "func (pl *Plane) SessionSnapshot(")
	if !ok {
		t.Fatalf("readface.go 里找不到 SessionSnapshot——跨包读面必须走它（改名/搬家时回来改这条判据）")
	}
	if !strings.Contains(body, "pl.mu.Lock()") {
		t.Errorf("SessionSnapshot 的函数体里没有 pl.mu.Lock()——它必须自己取锁，而不是要求调用方持锁")
	}

	// ③④ 消费面：用例层只许调自取锁的入口。
	snapHits, exportedHits, files := 0, 0, 0
	for _, p := range goFilesUnder(t, filepath.Join(root, "internal", "app", "session")) {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		files++
		s := stripGoComments(string(mustReadFile(t, p)))
		snapHits += strings.Count(s, "SessionSnapshot(")
		exportedHits += strings.Count(s, ".SessionResp(")
	}
	if files == 0 {
		t.Fatalf("svcsession 生产文件一个都没扫到——扫描面失效")
	}
	if snapHits == 0 {
		t.Errorf("svcsession 没有调用 SessionSnapshot——读面又回到了裸读路径")
	}
	if exportedHits != 0 {
		t.Errorf("svcsession 仍在调用 .SessionResp(——它已收成包内方法，跨包调用不可能持锁")
	}
}

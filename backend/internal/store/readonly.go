package store

import (
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/pkg/dbclient"
)

// 本文件是**只读打开**（`mate doctor` 的唯一入口）。
//
// 为什么不能直接用 `Open`：`Open` 会先跑自管迁移内核（建表/加列 = 写库）。体检命令的语义是
// "看一眼现状"，不是"顺手把它升到最新 schema"——一个 v10 的库在体检里应当如实报
// "读不到 providers/models 两张表"，而不是被升级、从此不能回退到旧二进制。

// OpenReadOnly 以 SQLite `mode=ro` 打开一个**已存在**的库：不迁移、不建表、写入当场失败。
//
// 返回错误的三种情形（调用方必须区分对待，`mate doctor` 把它们翻成"还没建库"与"库坏了"）：
//   - 路径为空（空路径在 dbclient 里会退化成内存库，那是另一种东西）；
//   - 文件不存在 / 是目录 / 长度为 0（都不是可用的库）；
//   - `mode=ro` 打开失败（WAL 面不可读、文件不是 SQLite 库…）。
//
// 已知边界（如实写下来，因为它看起来像"什么都没写"）：WAL 库的只读连接仍可能让 SQLite
// 重建 `-shm`/`-wal` 这两个**共享内存面**文件。它们不是数据面，`mate.db` 本身一个字节都不会
// 被改（写入会以 `attempt to write a readonly database` 当场失败）；这与
// `mate doctor` 的 `checkHomeWritable` 往 home 里写一次探针同属"只在 MATE_HOME 之内落笔"。
func OpenReadOnly(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("store: 只读打开需要库文件路径（空路径会退化成内存库）")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("store: 库文件不可读 %s：%w", path, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("store: %s 是目录，不是库文件", path)
	}
	if fi.Size() == 0 {
		return nil, fmt.Errorf("store: %s 长度为 0（不是可用的库：还没跑过 serve/migrate？）", path)
	}
	db, err := dbclient.Open(path + "?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("store: 只读打开 %s 失败（不跑迁移、也不会替你修它）：%w", path, err)
	}
	s := New(db)
	// path 必须留在句柄上：只读句柄也要能回答"库文件在哪"——`MigrationBackupPaths*`（备份扫描）
	// 与 doctor 的「迁移备份」那一条都按它拼路径。
	//
	// 不设它的后果是**静默**的（2026-10-14 实测）：扫描函数开头就是 `s.path == "" → nil`，
	// 于是 `mate doctor` 在本机明明有备份时也会说"没有找到备份"，把用户推向"回退窗口已关闭"
	// 的结论（那会让人去做一次本来不必要的密钥重设）。修在这里而不是给扫描函数加豁免：
	// 空路径的语义是"内存库"，不该被当成"不知道库文件在哪"。
	s.path = path
	return s, nil
}

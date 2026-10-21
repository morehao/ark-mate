package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/domain/session/event"
	"github.com/morehao/ark-mate/internal/domain/session/sessionrow"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
)

// migrate 把 transcript 目录（默认 ~/.ark-mate/transcripts）全量导入 SQLite（P1 迁移关键路径）：
//   - 解析全部 *.jsonl（复用 `domain/session` 的复读器——transcript 的 wire 形状归会话域，见 P3b-2b；坏文件告警跳过）；
//   - 单事务导入（store.ImportBatch：按会话逐行分配**会话内位次** → events → sessions → 数量校验 → 导入标记）；
//   - 幂等：同目录指纹已导入过则跳过；任一步失败整体回滚（原 transcript 目录不动）。
func Migrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 库路径（默认 MATE_DB 或 ~/.ark-mate/mate.db）")
	transcriptsDir := fs.String("transcripts", "", "transcript 目录（默认 MATE_TRANSCRIPTS 或 ~/.ark-mate/transcripts）")
	_ = fs.Parse(args)

	cfg, _ := config.MustLoad()
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *transcriptsDir != "" {
		cfg.TranscriptDir = *transcriptsDir
	}

	files, err := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: 扫描 transcript 目录: %v\n", err)
		os.Exit(1)
	}
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Printf("migrate: %s 下无 .jsonl transcript，无事可做\n", cfg.TranscriptDir)
		return
	}
	marker := fingerprintTranscripts(files)

	items := make([]store.ImportItem, 0, len(files))
	skipped, lineCount := 0, 0
	for _, path := range files {
		data, ok := session.ReadTranscriptFile(path)
		if !ok {
			// v1 格式（事件行没有 id/seq 身份）与损坏文件走同一条出口，但 v1 是**可预期**的那种：
			// 它的处置出口是 `mate transcripts prune --older-than 1s`（装载面已打 WARN + 提示）。
			fmt.Printf("migrate: 跳过不可读文件 %s（损坏或 v1 格式；后者见 `mate transcripts prune`）\n", filepath.Base(path))
			skipped++
			continue
		}
		events := make([]event.Item, 0, len(data.Lines))
		for i, l := range data.Lines {
			// 身份必须随数据来（v17）：**不代铸**——代铸出来的 id 与用户手里那份文件对不上，
			// 而"同一支 id 指向同一条事件"正是导入判重与诊断引用的前提。
			if l.ID == "" {
				fmt.Fprintf(os.Stderr,
					"migrate: %s 第 %d 条事件缺 id（v17 起 transcript 事件行必须带 `evt_…` 身份）——"+
						"旧格式请用 `mate transcripts prune --older-than 1s` 处置\n", filepath.Base(path), i+1)
				os.Exit(1)
			}
			b, mErr := json.Marshal(l.Payload)
			if mErr != nil {
				continue
			}
			events = append(events, event.Item{ID: l.ID, Type: l.Type, PayloadJSON: string(b)})
		}
		lineCount += len(events)
		items = append(items, store.ImportItem{
			Session: sessionrow.Item{
				ID:        data.SessionID,
				RuntimeID: data.RuntimeID,
				Title:     data.Title,
				Status:    data.Status,
				CreatedAt: data.CreatedAt,
				UpdatedAt: data.UpdatedAt,
			},
			Events: events,
		})
	}
	if len(items) == 0 {
		fmt.Println("migrate: 无可用 transcript，退出")
		return
	}

	if dir := filepath.Dir(cfg.DBPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: 创建库目录: %v\n", err)
			os.Exit(1)
		}
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: 打开库 %s: %v\n", cfg.DBPath, err)
		os.Exit(1)
	}
	defer st.Close()

	ev, sf, err := st.ImportBatch(context.Background(), items, marker, len(items), lineCount)
	if errors.Is(err, store.ErrAlreadyImported) {
		fmt.Printf("migrate: %s 已导入过该目录（幂等跳过）\n", cfg.DBPath)
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: 导入失败（已整体回滚，原 transcript 目录未动）: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✔ migrate: %d 会话 / %d 事件 已导入 → %s（跳过坏文件 %d）\n", sf, ev, cfg.DBPath, skipped)
}

// fingerprintTranscripts 目录指纹（文件名+大小，稳定序）——导入幂等标记。
func fingerprintTranscripts(files []string) string {
	h := sha256.New()
	for _, p := range files {
		if st, err := os.Stat(p); err == nil {
			_, _ = fmt.Fprintf(h, "%s:%d\n", filepath.Base(p), st.Size())
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

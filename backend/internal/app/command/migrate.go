package command

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

	"github.com/morehao/ark-mate/internal/manager"
	"github.com/morehao/ark-mate/internal/store"
)

// migrate 把 transcript 目录（默认 ~/.forge/transcripts）全量导入 SQLite（P1 迁移关键路径）：
//   - 解析全部 *.jsonl（复用 manager 复读器；坏文件告警跳过）；
//   - 单事务导入（store.ImportBatch：全局 seq 重分配 → events → conversations → 数量校验 → 导入标记）；
//   - 幂等：同目录指纹已导入过则跳过；任一步失败整体回滚（原 transcript 目录不动）。
func migrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 库路径（默认 FORGE_DB 或 ~/.forge/forge.db）")
	transcriptsDir := fs.String("transcripts", "", "transcript 目录（默认 FORGE_TRANSCRIPTS 或 ~/.forge/transcripts）")
	_ = fs.Parse(args)

	cfg, _ := mustConfig()
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
		data, ok := manager.ReadTranscriptFile(path)
		if !ok {
			fmt.Printf("migrate: 跳过坏文件 %s\n", filepath.Base(path))
			skipped++
			continue
		}
		events := make([]store.Event, 0, len(data.Lines))
		for _, l := range data.Lines {
			b, mErr := json.Marshal(l.Payload)
			if mErr != nil {
				continue
			}
			events = append(events, store.Event{Type: l.Type, PayloadJSON: string(b)})
		}
		lineCount += len(events)
		items = append(items, store.ImportItem{
			Conversation: store.Conversation{
				ID:          data.SessionID,
				AgentID:     data.AgentID,
				RuntimeHint: data.RuntimeHint,
				Title:       data.Title,
				Status:      data.Status,
				CreatedAt:   data.CreatedAt,
				UpdatedAt:   data.UpdatedAt,
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

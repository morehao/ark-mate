package maintenance

import (
	"flag"
	"fmt"
	"github.com/morehao/ark-mate/internal/config"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// transcriptPrune 显式清理 transcript 目录中超过 --older-than 的文件（ADR-5：
// transcript 为审计/回滚保留，不设自动删除；清理仅经本命令，默认目录 MATE_TRANSCRIPTS）。
func TranscriptsPrune(args []string) {
	fs := flag.NewFlagSet("transcripts prune", flag.ExitOnError)
	olderThan := fs.String("older-than", "", "删除修改时间早于此时长的 *.jsonl（必填；支持 30d / 168h / 72h30m 等）")
	dir := fs.String("dir", "", "transcript 目录（默认 MATE_TRANSCRIPTS 或 ~/.ark-mate/transcripts）")
	_ = fs.Parse(args)
	cfg, _ := config.MustLoad()
	if *dir != "" {
		cfg.TranscriptDir = *dir
	}
	age, err := parseAge(*olderThan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "transcripts prune: --older-than 非法: %v（示例 30d / 168h）\n", err)
		os.Exit(2)
	}
	files, err := filepath.Glob(filepath.Join(cfg.TranscriptDir, "*.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "transcripts prune: %v\n", err)
		os.Exit(1)
	}
	cutoff := time.Now().Add(-age)
	deleted, kept := 0, 0
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.ModTime().Before(cutoff) {
			if err := os.Remove(p); err != nil {
				fmt.Fprintf(os.Stderr, "transcripts prune: 删除 %s: %v\n", filepath.Base(p), err)
				continue
			}
			deleted++
			continue
		}
		kept++
	}
	fmt.Printf("✔ transcripts prune: 删除 %d（>%s）、保留 %d（%s）\n", deleted, age.String(), kept, cfg.TranscriptDir)
}

// parseAge 解析清理时长：支持 Go duration（如 168h/72h30m）与天（30d/7d 等）；空/非法报错。
func parseAge(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("空值")
	}
	if len(s) > 1 && (s[len(s)-1] == 'd' || s[len(s)-1] == 'D') {
		days, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("非法天数 %q", s)
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("非法时长 %q", s)
	}
	return d, nil
}

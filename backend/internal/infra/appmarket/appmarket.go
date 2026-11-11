// Package appmarket 实现「应用市场」的清单（market listing）拉取、缓存与内置兜底。
//
// 它是叠在应用体系二期 C（运行期装卸）之上的**分发与发现层**：让「装个包就多一个应用」这件事
// 可以被浏览、被安装、被管理。本包只回答一个问题——**「有哪些可安装的应用包，以及它们从哪装」**：
//
//   - Listing/MarketFile 是清单的数据形状（§3 数据契约）；
//   - Parse 严格解析并校验清单（未知字段拒绝、id 复用 capability 词法、source 类型/来源根校验）；
//   - Market 提供三级兜底（远端 → 磁盘缓存 → 内置 go:embed）的清单读取（见 market.go）。
//
// 本包**只管清单是什么**，不碰「装没装 / 能不能装 / 有没有更新」这类派生态——那需要 store + runtime，
// 归 `svcapp.MarketApps`（它持有 App 定义表与运行期端口）。分层：本包只依赖标准库 + `apppkg`
// （复用其 id 词法判定与 withinDir 来源根校验），不 import 任何上层包，不执行包代码。
//
// 与 `apppkg.Manifest` 的分工：Manifest 描述「一个包提供什么」（在包内）；Listing 描述「市场中
// 一个可装的应用包在哪、叫什么」（在市场清单里指向包及其来源）。两者靠同一套 id 词法关联。
package appmarket

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/morehao/ark-mate/internal/core/apppkg"
)

// ============ 数据结构（§3 数据契约，钉死字段）============

// MarketFile 是一份市场清单文件（内置 market.json 与远端 registry.json 同 schema）。
type MarketFile struct {
	// Version 清单 schema 版本（当前为 1）。
	Version int `json:"version"`
	// Apps 可安装的应用条目。
	Apps []Listing `json:"apps"`
}

// Listing 是市场清单中的一个应用条目：指向一个可安装的应用包及其元数据。
//
// 它与 `apppkg.Manifest`（包内分发清单）是两个层次：Listing 在市场清单里「指向」包（source），
// Manifest 在包内「声明」包提供什么。
type Listing struct {
	// ID 与 apppkg.Manifest.ID / capability.App.ID 同词法 `^[a-z0-9]+(-[a-z0-9]+)*$`，≤64 位。
	ID string `json:"id"`
	// Name 展示名（1-40 字）。
	Name string `json:"name"`
	// Icon 图标键（内置图标集里的键，前端静态映射）；可选。
	Icon string `json:"icon,omitempty"`
	// Summary 一句话摘要（中/英），至少 zh。
	Summary Summary `json:"summary"`
	// Version 推荐版本（列表以此为「最新」，C8 更新判定用）；必填，非空。
	Version string `json:"version"`
	// Homepage 项目主页 URL；可选。
	Homepage string `json:"homepage,omitempty"`
	// Source 从哪里装（本地包目录 / 远端包地址）；必填。
	Source Source `json:"source"`
	// MinCoreVersion 门槛（复用 apppkg 版本门口径）；可选，空 = 不设门槛。
	MinCoreVersion string `json:"min_core_version,omitempty"`
	// Tags 标签（≤8 个，每个 ≤20 字）；可选。
	Tags []string `json:"tags,omitempty"`
	// Tagline 一句话能力句（v42，≤40 字）：市场卡片「装前预览」用；空则前端回退 summary.zh。
	Tagline string `json:"tagline,omitempty"`
	// Examples 可直接发送的示例话术（v42，≤6 条、每条 ≤100 字）。
	Examples []string `json:"examples,omitempty"`
	// UsageGuide 「怎么用」正文（v42，≤600 字）。
	UsageGuide string `json:"usage_guide,omitempty"`
}

// Summary 是多语言的一句话摘要（至少 zh）。
type Summary struct {
	ZH string `json:"zh"`
	EN string `json:"en,omitempty"`
}

// Source 是一个应用包的来源声明。
type Source struct {
	// Type 来源类型：`dir`（随发行包分发的本地目录）| `url`（C8 远程下载，本期置灰）。
	Type string `json:"type"`
	// Dir 当 Type==dir 时的包目录，相对「来源根」解析（如 `apps/weather-query`）。
	Dir string `json:"dir,omitempty"`
	// URL 当 Type==url 时的远端包地址（本期仅留字段，不实现下载）。
	URL string `json:"url,omitempty"`
	// SHA256 当 Type==url 时的完整性哈希（本期留字段，C8 用）。
	SHA256 string `json:"sha256,omitempty"`
}

// 来源类型枚举（本期只做 dir；url 留字段、UI 置灰，属 C8）。
const (
	SourceTypeDir = "dir"
	SourceTypeURL = "url"
)

// 硬上限（防失控清单；超出即拒绝）。
const (
	MaxListingNameRunes    = 40
	MaxListingSummaryRunes = 120
	MaxListingTags         = 8
	MaxListingTagRunes     = 20
	MaxListingVersionRunes = 64
	// 使用说明（v42，与 capability/apppkg 同量级）。
	MaxListingTaglineRunes = 40
	MaxListingExamples     = 6
	MaxListingExampleRunes = 100
	MaxListingUsageRunes   = 600
	maxListingIDLen        = 64
)

// listingIDRE 与 capability.App / apppkg 的 id 词法同口径（不重写：这里只做本包的结构校验，
// 真正的 id 词法以 apppkg/capability 为准，见 Validate 里调用 validateListingID）。
var listingIDRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Parse 从 JSON 字节解析市场清单并校验（解析 + Validate 一步到位）。
//
// 严格解码：未知字段拒绝（`DisallowUnknownFields`）——清单是对外分发契约，容忍未知字段会让拼错
// 字段名静默失效（与 `apppkg.ParseManifest` 同一纪律）。
//
// sourceRoots 是允许的**来源根**集合（绝对路径，M-A7 由装配根注入；此处用于校验 dir 条目的
// `source.dir` 解析后是否落在某个来源根内，防市场清单指向 `/etc` 这类系统路径被当包装载）。
// 传空集合时**只做结构校验**，不做来源根越界校验（由调用方/装配根决定是否强制；M-A1 阶段
// 单测常用空集合）。
func Parse(data []byte, sourceRoots []string) (*MarketFile, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var f MarketFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("appmarket: 解析市场清单失败: %w", err)
	}
	if err := f.Validate(sourceRoots); err != nil {
		return nil, err
	}
	return &f, nil
}

// Validate 是市场清单的**唯一闸门**（纯函数；解析后与读取前共用）。它会规范化部分字段（去首尾
// 空白），调用方应以返回值之后的值为准。
func (f *MarketFile) Validate(sourceRoots []string) error {
	if f == nil {
		return fmt.Errorf("appmarket: 市场清单为空")
	}
	if f.Version != 1 {
		return fmt.Errorf("appmarket: 不支持的清单 schema 版本 %d（当前仅支持 1）", f.Version)
	}
	seen := make(map[string]bool, len(f.Apps))
	for i := range f.Apps {
		l := &f.Apps[i]
		if err := l.validate(sourceRoots); err != nil {
			return fmt.Errorf("appmarket: apps[%d]: %w", i, err)
		}
		if seen[l.ID] {
			return fmt.Errorf("appmarket: 应用 id %q 在清单中重复", l.ID)
		}
		seen[l.ID] = true
	}
	return nil
}

// validate 校验单条 Listing 并规范化。
func (l *Listing) validate(sourceRoots []string) error {
	l.ID = strings.TrimSpace(l.ID)
	if err := validateListingID(l.ID); err != nil {
		return err
	}

	l.Name = strings.TrimSpace(l.Name)
	if l.Name == "" {
		return fmt.Errorf("appmarket %q: name 不能为空", l.ID)
	}
	if r := []rune(l.Name); len(r) > MaxListingNameRunes {
		return fmt.Errorf("appmarket %q: name 超过 %d 字", l.ID, MaxListingNameRunes)
	}

	l.Icon = strings.TrimSpace(l.Icon)

	l.Summary.ZH = strings.TrimSpace(l.Summary.ZH)
	l.Summary.EN = strings.TrimSpace(l.Summary.EN)
	if l.Summary.ZH == "" {
		return fmt.Errorf("appmarket %q: summary.zh 不能为空", l.ID)
	}
	if r := []rune(l.Summary.ZH); len(r) > MaxListingSummaryRunes {
		return fmt.Errorf("appmarket %q: summary.zh 超过 %d 字", l.ID, MaxListingSummaryRunes)
	}
	if r := []rune(l.Summary.EN); len(r) > MaxListingSummaryRunes {
		return fmt.Errorf("appmarket %q: summary.en 超过 %d 字", l.ID, MaxListingSummaryRunes)
	}

	l.Version = strings.TrimSpace(l.Version)
	if l.Version == "" {
		return fmt.Errorf("appmarket %q: version 不能为空", l.ID)
	}
	if r := []rune(l.Version); len(r) > MaxListingVersionRunes {
		return fmt.Errorf("appmarket %q: version 超过 %d 字符", l.ID, MaxListingVersionRunes)
	}

	l.Homepage = strings.TrimSpace(l.Homepage)
	l.MinCoreVersion = strings.TrimSpace(l.MinCoreVersion)

	// 使用说明（v42）：与 capability/apppkg 同一套上限。
	l.Tagline = strings.TrimSpace(l.Tagline)
	if r := []rune(l.Tagline); len(r) > MaxListingTaglineRunes {
		return fmt.Errorf("appmarket %q: tagline 超过 %d 字", l.ID, MaxListingTaglineRunes)
	}
	if len(l.Examples) > MaxListingExamples {
		return fmt.Errorf("appmarket %q: examples 最多 %d 条（当前 %d）", l.ID, MaxListingExamples, len(l.Examples))
	}
	examples := make([]string, 0, len(l.Examples))
	for _, ex := range l.Examples {
		ex = strings.TrimSpace(ex)
		if ex == "" {
			continue
		}
		if r := []rune(ex); len(r) > MaxListingExampleRunes {
			return fmt.Errorf("appmarket %q: 单条示例超过 %d 字", l.ID, MaxListingExampleRunes)
		}
		examples = append(examples, ex)
	}
	if len(examples) == 0 {
		l.Examples = nil
	} else {
		l.Examples = examples
	}
	l.UsageGuide = strings.TrimSpace(l.UsageGuide)
	if r := []rune(l.UsageGuide); len(r) > MaxListingUsageRunes {
		return fmt.Errorf("appmarket %q: usage_guide 超过 %d 字", l.ID, MaxListingUsageRunes)
	}

	if err := l.validateSource(sourceRoots); err != nil {
		return err
	}

	// tags：去空白 + 去重 + 上限，每项 ≤20 字。
	if len(l.Tags) > MaxListingTags {
		return fmt.Errorf("appmarket %q: tags 最多 %d 个（当前 %d）", l.ID, MaxListingTags, len(l.Tags))
	}
	seenTag := make(map[string]bool, len(l.Tags))
	out := make([]string, 0, len(l.Tags))
	for _, tg := range l.Tags {
		tg = strings.TrimSpace(tg)
		if tg == "" {
			continue
		}
		if r := []rune(tg); len(r) > MaxListingTagRunes {
			return fmt.Errorf("appmarket %q: tag %q 超过 %d 字", l.ID, tg, MaxListingTagRunes)
		}
		if seenTag[tg] {
			continue
		}
		seenTag[tg] = true
		out = append(out, tg)
	}
	l.Tags = out
	return nil
}

// validateSource 校验来源声明：type ∈ {dir,url}；dir 条目要求 dir 非空且（若给了来源根）落在
// 某个来源根内。
func (l *Listing) validateSource(sourceRoots []string) error {
	l.Source.Type = strings.TrimSpace(l.Source.Type)
	switch l.Source.Type {
	case SourceTypeDir:
		l.Source.Dir = strings.TrimSpace(l.Source.Dir)
		if l.Source.Dir == "" {
			return fmt.Errorf("appmarket %q: source.dir 不能为空（type=dir）", l.ID)
		}
		if strings.HasPrefix(l.Source.Dir, "/") || strings.HasPrefix(l.Source.Dir, "\\") {
			return fmt.Errorf("appmarket %q: source.dir %q 须为相对来源根的路径，不能是绝对路径", l.ID, l.Source.Dir)
		}
		// 来源根越界校验（防指向 /etc）：仅当调用方给了来源根集合时强制。
		if len(sourceRoots) > 0 {
			if !withinAnyRoot(sourceRoots, l.Source.Dir) {
				return fmt.Errorf("appmarket %q: source.dir %q 解析后不在任何允许来源根内（拒绝，防指向系统路径）", l.ID, l.Source.Dir)
			}
		}
	case SourceTypeURL:
		l.Source.URL = strings.TrimSpace(l.Source.URL)
		if l.Source.URL == "" {
			return fmt.Errorf("appmarket %q: source.url 不能为空（type=url）", l.ID)
		}
		// url 下载属 C8，本期只保留字段；此处不校验可下载性。
	default:
		return fmt.Errorf("appmarket %q: source.type %q 非法（须 dir 或 url）", l.ID, l.Source.Type)
	}
	return nil
}

// withinAnyRoot 报告相对路径 rel 解析到某个来源根后是否落在该根内（复用 apppkg.WithinDir）。
func withinAnyRoot(roots []string, rel string) bool {
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		// 用 filepath.Join 拼出候选绝对路径再判越界（同时防 `..` 上跳出根）。
		if apppkg.WithinDir(root, filepath.Join(root, filepath.FromSlash(rel))) {
			return true
		}
	}
	return false
}

// validateListingID 用本包的 id 正则做结构层校验；与 capability/apppkg 的 id 词法同口径
// （`^[a-z0-9]+(-[a-z0-9]+)*$`，≤64 位）。真正深层的 App 校验仍归 capability，此处只挡明显非法。
func validateListingID(id string) error {
	if id == "" {
		return fmt.Errorf("appmarket: id 不能为空")
	}
	if len(id) > maxListingIDLen {
		return fmt.Errorf("appmarket %q: id 超过 %d 位", id, maxListingIDLen)
	}
	if !listingIDRE.MatchString(id) {
		return fmt.Errorf("appmarket %q: id 非法（须小写字母/数字，用连字符分段，形如 weather-query，≤%d 位）", id, maxListingIDLen)
	}
	return nil
}

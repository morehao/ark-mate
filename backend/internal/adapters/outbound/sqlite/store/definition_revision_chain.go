package store

// ——— definition_revisions 的哈希链（v32 / T4）———
//
// 一条链只有一个作者（写入走 `appendDefinitionRevision`），只有一处校验（`VerifyDefinitionRevisionChain`），
// 两者共用同一个哈希函数 —— 这是本文件存在的全部意义：**写入算法与验证算法必须是同一段代码**，
// 各写一遍必然在某次"改进归一化"之后静默分叉（那时链会整体报断，而人只会以为"校验器坏了"）。
//
// 归一化的三条规则（判据都在测试里，不靠注释约定）：
//
//	① 字段集合固定：`prev_hash` / `kind` / `definition_id` / `action` / `actor` / `at` / `digest`。
//	  `seq` **不进哈希**——它是插入时由 AUTOINCREMENT 分配的，写的时候还没有；而且它篡改不了
//	  链条：把两行换个位置，后一行的 `prev_hash` 立刻对不上（顺序信息由链本身保护）。
//	② 每个字段**长度前缀**（`<len>:<value>`）后拼接：`a|b` 与 `a` + `|b` 在裸拼接下无法区分，
//	  长度前缀让任意组合只有一种编码（这正是 T4 说的"hash 输入要规范化"）。
//	③ 时间取**落库文本**（`model.FormatTime`，RFC3339Nano(UTC)）：库里的那一串字符就是被哈希的
//	  那一串，不从 `time.Time` 再格式化一遍——两处各算一遍迟早分叉。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"gorm.io/gorm"
)

// DefinitionRevisionGenesisHash 创世条的前驱（链的起点）。
//
// 刻意不是 64 个 0：那看起来像"一个真的哈希"，而校验器要能一眼分辨"这是起点"与"这是上一行的
// 哈希"。用一个人造哨兵值，撞车概率为零，且日志里读得懂。
const DefinitionRevisionGenesisHash = "genesis"

// DefinitionRevisionChainBreak 校验发现的**第一条**坏行。
type DefinitionRevisionChainBreak struct {
	// Seq 坏行的位次（AUTOINCREMENT；链上唯一可定位的东西）。
	Seq int64
	// Kind / DefinitionID 坏行说的是哪条定义（日志里能直接指回去）。
	Kind         string
	DefinitionID string
	// Reason 断法：
	//   "link"    = 这一行的 `prev_hash` 不等于前一行的 `hash`（有人删/插/换过行）；
	//   "content" = 这一行的内容算出来的哈希与存着的 `hash` 不等（有人改过字段）；
	//   "gap"     = 链开始之后又出现一条没有哈希的行（有人把 hash 清空了）。
	Reason string
	// Want / Got 判据的期望值与实际值（哈希 hex 或前驱；日志与测试断言都用它）。
	Want string
	Got  string
}

// 断法取值（`DefinitionRevisionChainBreak.Reason`）。
const (
	ChainBreakLink    = "link"
	ChainBreakContent = "content"
	ChainBreakGap     = "gap"
)

// definitionRevisionHash 一条审计行的链哈希（sha256 hex，归一化见文件头三条规则）。
func definitionRevisionHash(rev model.DefinitionRevisionEntity) string {
	fields := []string{
		rev.PrevHash, rev.Kind, rev.DefinitionID, rev.Action, rev.Actor,
		model.FormatTime(rev.At), rev.Digest,
	}
	h := sha256.New()
	for _, f := range fields {
		// 长度前缀：`<len>:<value>`。WriteString 与 Sum 都不会失败（hash.Hash 的约定）。
		h.Write([]byte(strconv.Itoa(len(f))))
		h.Write([]byte(":"))
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// appendDefinitionRevision **在调用方事务内**追加一条带链的审计行。
//
// 取前驱与插行必须同事务：分开写就会出现"读到 H、插进去时前驱已经是 H'"的窗口，链在那一处
// 分叉。单进程内成立的两个前提，都不是本文件提供的：
//   - 连接池 **`MaxOpenConns=1`**（单写者模型，`internal/adapters/outbound/sqlite/db`）：并发提交
//     在连接上就排队，两件事务不可能同时读到同一个前驱；
//   - 文件库另注入 `_txlock=immediate`（`BEGIN IMMEDIATE`，同一份 db 装配）：万一将来放开连接数，
//     事务也会在**开始时**拿写锁，而不是等到第一次写才升级锁。
//
// 多实例部署不在今天的形态里（C26）；真要多实例，得先把写锁挪到进程外——那时"取前驱 + 插行"
// 这一步必须在同一个跨进程临界区里，否则链会在两个实例之间分叉。
func (s *Store) appendDefinitionRevision(ctx context.Context, tx *gorm.DB, rev model.DefinitionRevisionEntity) error {
	prev, err := s.lastDefinitionRevisionHash(ctx, tx)
	if err != nil {
		return err
	}
	rev.PrevHash = prev
	rev.Hash = definitionRevisionHash(rev)
	return s.definitionRevision.WithTx(tx).Append(ctx, &rev)
}

// lastDefinitionRevisionHash 当前链尾的哈希；空表、或链尾还是 v32 之前的行（hash 为空）→ 创世哨兵。
func (s *Store) lastDefinitionRevisionHash(ctx context.Context, tx *gorm.DB) (string, error) {
	var tail []string
	if err := s.definitionRevision.WithTx(tx).DB(ctx).Model(&model.DefinitionRevisionEntity{}).
		Order("seq DESC").Limit(1).Pluck("hash", &tail).Error; err != nil {
		return "", err
	}
	if len(tail) == 0 || tail[0] == "" {
		return DefinitionRevisionGenesisHash, nil
	}
	return tail[0], nil
}

// VerifyDefinitionRevisionChain 只读校验：从头遍历，返回第一条坏行（链完好 → nil）与被校验的行数。
//
// 行为（三条都得在）：
//
//	① v32 之前的行（`hash` 为空）是**前缀**：链还没开始，跳过就好；
//	② 链开始之后又冒出空哈希 → `gap`（有人清空了某行的 hash 想让它"消失"）；
//	③ 每条有哈希的行都要过两关：`prev_hash` 等于前一条的 `hash`（link），并且按内容重算等于
//	   存下来的 `hash`（content）。第二关是"改过字段"的唯一判据——只查链不看内容的话，
//	   改一个字都发现不了（下一行的 prev 还指着旧哈希，但旧哈希本身没变）。
//
// 只读：本函数不开事务、不写任何东西。
func (s *Store) VerifyDefinitionRevisionChain(ctx context.Context) (int, *DefinitionRevisionChainBreak, error) {
	var rows model.DefinitionRevisionEntityList
	if err := s.definitionRevision.DB(ctx).Model(&model.DefinitionRevisionEntity{}).
		Order("seq ASC").Find(&rows).Error; err != nil {
		return 0, nil, err
	}
	want := DefinitionRevisionGenesisHash
	checked := 0
	started := false
	for _, r := range rows {
		if r.Hash == "" {
			if !started {
				continue // v32 之前的历史行：链的前缀，不参与校验
			}
			return checked, &DefinitionRevisionChainBreak{
				Seq: r.Seq, Kind: r.Kind, DefinitionID: r.DefinitionID,
				Reason: ChainBreakGap, Want: want, Got: "",
			}, nil
		}
		if r.PrevHash != want {
			return checked, &DefinitionRevisionChainBreak{
				Seq: r.Seq, Kind: r.Kind, DefinitionID: r.DefinitionID,
				Reason: ChainBreakLink, Want: want, Got: r.PrevHash,
			}, nil
		}
		if got := definitionRevisionHash(r); got != r.Hash {
			return checked, &DefinitionRevisionChainBreak{
				Seq: r.Seq, Kind: r.Kind, DefinitionID: r.DefinitionID,
				Reason: ChainBreakContent, Want: r.Hash, Got: got,
			}, nil
		}
		want = r.Hash
		started = true
		checked++
	}
	return checked, nil, nil
}

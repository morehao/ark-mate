package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/platform/object/objid"
	wkrow "github.com/morehao/ark-mate/internal/domain/workspace/row"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/dao"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/model"
	"github.com/morehao/golib/dbaccess/gormdao"
	"gorm.io/gorm"
)

// 本文件是**工作空间登记表**（v14）的持久层门面。
//
// 这里承载的全是**跨表不变量**，所以它们既不在 dao（跨两张表）、也不在 manager（要事务）：
//
//   - **登记唯一**：一个规范化目录至多一行。执行者是 `path` 上的库级唯一索引——并发两次
//     创建会各自读到"不存在"然后各插一行，只有库能挡住它（设计 ADR-1）；
//   - **顺序对账**：重排后 `list_order` 必须是 `0..n-1` 的一个排列。行数与集合都不符即
//     回滚报错——不"尽力而为"地留一个空洞顺序，那种库下次重排时会自己漂移；
//   - **删登记不删会话**：删一行登记只把该工作空间下会话的 `workspace_id` 清空，目录、
//     文件、会话历史一概不动（设计 ADR-6）。这条必须在**同一个事务**里，否则中断会留下
//     "登记没了、会话还指着一个不存在的 id"的半截状态。
//
// 单表读写经 `internal/adapters/outbound/sqlite/dao` 的泛型方法 + `dao.WorkspaceCond`；**业务条件**（展示顺序、默认档、
// "最大顺序 +1"的排列约定）住在这一层——DAO 只认识列，不认识"展示顺序"这种事。

// workspaceListOrder 是登记的展示顺序（list_order 升序；同序时按 created_at 兜底稳定）。
//
// 它是**读面的口径**，所以放在 store 而不是 DAO：DAO 不该知道"用户看到的是哪个顺序"。
const workspaceListOrder = "list_order ASC, created_at ASC"

// ErrWorkspacePathTaken 并发创建时库级唯一索引拦下的信号（调用方据此重读既有行，
// 把"输了竞态"变成"返回既有那一行"——get-or-create 的语义必须对并发也成立）。
var ErrWorkspacePathTaken = errors.New("store: workspace path already registered")

// WorkspaceProvision 是**默认工作空间**的建立参数（装配期由配置给出）。
type WorkspaceProvision struct {
	// Dir 默认工作空间目录（空 = 不建立，建会话未指定 cwd 时报 workspace_required）。
	//
	// 它是 `MATE_WORKSPACE_DIR` 下的 `default/` 子目录（不是那个根本身）：根下面还会按需
	// 放别的东西，把根登记成一个工作空间会让"平台代建根恒在生效白名单内"这条规则
	// 与"用户的工作目录"混成同一个东西。
	Dir string
}

// BootstrapDefaultWorkspace 在**登记表为空**时建立并登记默认工作空间；否则原样返回既有状态。
//
// 三条语义，每条都是判据（设计 ADR-4/ADR-6）：
//   - **只在空表时创建**：删掉默认工作空间之后不再自动重建。自动重建会让"我删掉它"这个
//     动作永远无效——用户删了它、下次启动它又回来，而界面上没有任何东西解释这件事；
//   - **幂等**：已有默认工作空间 → 直接返回它，不解析目录、不建目录；
//   - **可关闭**：dir 为空（代建关闭）→ 不建立，返回 nil。这不是错误：该类部署
//     强制显式传 cwd（既有的 `workspace_required`）。
//
// **建目录是文件系统副作用，事务回滚不了它**：目录先建、再写库；写库失败时目录留在磁盘上
// （空目录，无害），由 v14 的迁移说明与 doctor 如实登记，不假装能回滚。
func (s *Store) BootstrapDefaultWorkspace(ctx context.Context, dir string) (*wkrow.Item, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	n, err := s.workspaces.CountByCond(ctx, &dao.WorkspaceCond{})
	if err != nil {
		return nil, err
	}
	if n > 0 {
		// 表非空：可能是已有安装（用户在跑别的项目），也可能是默认被删过。
		// 两种情况都不自动创建——这正是"删了就不再回来"的实现点。
		row, err := s.defaultWorkspaceRow(ctx)
		return workspaceItemPtr(row), err
	}
	row, err := s.createDefaultWorkspace(ctx, dir)
	return workspaceItemPtr(row), err
}

// workspaceRows 按展示顺序读全部登记（单表读 + 业务顺序）。
func (s *Store) workspaceRows(ctx context.Context) (model.WorkspaceEntityList, error) {
	return s.workspaceRowsWith(ctx, s.workspaces)
}

// workspaceRowsTx 是事务内版本（重排要读"同一事务里的当前顺序"）。
func (s *Store) workspaceRowsTx(ctx context.Context, tx *gorm.DB) (model.WorkspaceEntityList, error) {
	return s.workspaceRowsWith(ctx, s.workspaces.WithTx(tx))
}

func (s *Store) workspaceRowsWith(ctx context.Context, d *dao.WorkspaceDao) (model.WorkspaceEntityList, error) {
	return d.GetListByCond(ctx, &dao.WorkspaceCond{
		BaseCond: &gormdao.BaseCond{OrderField: workspaceListOrder},
	})
}

// workspaceByPath 按规范化路径取一条登记（不存在 → (nil, nil)）。
//
// 空路径直接判"没有"：Cond 里的 Path 是可选条件，不早退会退化成"返回第一条登记"。
func (s *Store) workspaceByPath(ctx context.Context, path string) (*model.WorkspaceEntity, error) {
	return s.workspaceByPathWith(ctx, s.workspaces, path)
}

func (s *Store) workspaceByPathWith(ctx context.Context, d *dao.WorkspaceDao, path string) (*model.WorkspaceEntity, error) {
	if path == "" {
		return nil, nil
	}
	return d.GetByCond(ctx, &dao.WorkspaceCond{Path: path})
}

// defaultWorkspaceRow 取默认工作空间那一行（不存在 → (nil, nil)）。
//
// 「有且只有一个」由 `is_default = 1` 的部分唯一索引保证，所以这里不需要处理多行。
func (s *Store) defaultWorkspaceRow(ctx context.Context) (*model.WorkspaceEntity, error) {
	isDefault := true
	return s.workspaces.GetByCond(ctx, &dao.WorkspaceCond{IsDefault: &isDefault})
}

// maxWorkspaceOrder 返回当前最大顺序；**空表返回 -1**（不是 0）。
//
// 为什么空表是 -1 而不是 0：新登记的 list_order 是「最大顺序 + 1」，而顺序的约定是 `0..n-1`
// （见 MoveWorkspace 的对账）。若空表返回 0，第一条登记就是 1——库里从此带着一个从 1 开始的
// 序列，与重排后的 0..n-1 不一致，而那种不一致没有任何界面能显示出来。
func (s *Store) maxWorkspaceOrder(ctx context.Context) (int64, error) {
	var max int64
	err := s.db.WithContext(ctx).Model(&model.WorkspaceEntity{}).
		Select("COALESCE(MAX(list_order), -1)").Scan(&max).Error
	return max, err
}

// createDefaultWorkspace 建目录并登记默认工作空间（只由 BootstrapDefaultWorkspace 调用）。
func (s *Store) createDefaultWorkspace(ctx context.Context, dir string) (*model.WorkspaceEntity, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: 建立默认工作空间目录 %s: %w", dir, err)
	}
	canonical := dir
	if real, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		canonical = real
	}
	now := time.Now().UTC()
	row := model.WorkspaceEntity{
		ID:        objid.New(objid.Workspace),
		Path:      canonical,
		Title:     wkrow.DefaultTitle,
		IsDefault: true,
		ListOrder: 0,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.workspaces.Insert(ctx, &row); err != nil {
		// 并发/竞态：另一个进程刚建过 → 返回既有那一条（与 get-or-create 同一手法）。
		if isUniqueViolation(err) {
			if existing, gerr := s.defaultWorkspaceRow(ctx); gerr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("store: 登记默认工作空间: %w", err)
	}
	return &row, nil
}

// DefaultWorkspace 取默认工作空间（不存在 → (nil, false, nil)）。
func (s *Store) DefaultWorkspace(ctx context.Context) (wkrow.Item, bool, error) {
	row, err := s.defaultWorkspaceRow(ctx)
	if err != nil || row == nil {
		return wkrow.Item{}, false, err
	}
	return workspaceItemOf(*row), true, nil
}

// ListWorkspaces 按展示顺序列出全部登记。
func (s *Store) ListWorkspaces(ctx context.Context) ([]wkrow.Item, error) {
	rows, err := s.workspaceRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]wkrow.Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, workspaceItemOf(r))
	}
	return out, nil
}

// GetWorkspace 按 id 取一条登记（不存在 → (nil, false, nil)）。
func (s *Store) GetWorkspace(ctx context.Context, id string) (wkrow.Item, bool, error) {
	row, err := s.workspaces.GetByID(ctx, id)
	if err != nil || row == nil {
		return wkrow.Item{}, false, err
	}
	return workspaceItemOf(*row), true, nil
}

// WorkspaceByPath 按规范化路径取一条登记（不存在 → (nil, false, nil)）。
func (s *Store) WorkspaceByPath(ctx context.Context, path string) (wkrow.Item, bool, error) {
	row, err := s.workspaceByPath(ctx, path)
	if err != nil || row == nil {
		return wkrow.Item{}, false, err
	}
	return workspaceItemOf(*row), true, nil
}

// CreateWorkspace 登记一个工作空间（**get-or-create**：同路径已登记 → 返回既有那一行）。
//
// 为什么默认是 get-or-create 而不是报错：前端的"选一个目录建工作空间"这一步会被重复触发
// （用户多按一次、或「最近使用（未登记）」那一栏点两下），报错会让一次幂等意图看起来像失败。
// 需要"必须新建"的调用方传 distinctPath 检查（见 manager 的 strict 分支）。
func (s *Store) CreateWorkspace(ctx context.Context, path, title string) (wkrow.Item, error) {
	canonical := strings.TrimSpace(path)
	if canonical == "" {
		return wkrow.Item{}, errors.New("store: 工作空间路径不能为空")
	}
	if existing, err := s.workspaceByPath(ctx, canonical); err != nil {
		return wkrow.Item{}, err
	} else if existing != nil {
		return workspaceItemOf(*existing), nil
	}
	max, err := s.maxWorkspaceOrder(ctx)
	if err != nil {
		return wkrow.Item{}, err
	}
	now := time.Now().UTC()
	row := model.WorkspaceEntity{
		ID:        objid.New(objid.Workspace),
		Path:      canonical,
		Title:     strings.TrimSpace(title),
		IsDefault: false,
		ListOrder: max + 1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.workspaces.Insert(ctx, &row); err != nil {
		// 并发输了竞态：唯一索引拦下 → 重读既有行，仍然是 get-or-create 的语义。
		if isUniqueViolation(err) {
			if existing, gerr := s.workspaceByPath(ctx, canonical); gerr == nil && existing != nil {
				return workspaceItemOf(*existing), nil
			}
		}
		return wkrow.Item{}, fmt.Errorf("store: 登记工作空间 %s: %w", canonical, err)
	}
	return workspaceItemOf(row), nil
}

// RenameWorkspace 改写展示名（title 为空串 = 清空，回落到末级目录名渲染）。
func (s *Store) RenameWorkspace(ctx context.Context, id, title string) error {
	return s.workspaces.UpdateMap(ctx, id, map[string]any{
		"title":      strings.TrimSpace(title),
		"updated_at": model.NowTS(),
	})
}

// RelocateWorkspace 改写一条登记的**路径**（W4）：id 不变，`sessions` 一个字节都不动。
//
// 三件事刻意都在这一处：
//
//	① **只改 `workspaces.path`**：既有会话的 `cwd` 是**执行事实**（ACP 的 cwd、沙箱边界），
//	   而且它们的 transcript 就落在那个目录里。跟着搬等于把历史指到别处；
//	② **冲突显式报错**（`ErrWorkspacePathTaken`），不 get-or-create：改路径的意图是"这个工作空间
//	   现在指着那个目录"，如果那个目录已被**另一条登记**占着，静默返回那条既有行会让用户以为
//	   自己改成功了，而实际上他改的那个工作空间还在老地方——两个入口给出同一个"成功"；
//	③ **默认工作空间的路径不可改**（装配期钉死，ADR-4）：它是"建会话未指定 cwd"的落点。
func (s *Store) RelocateWorkspace(ctx context.Context, id, canonicalPath string) (bool, error) {
	row, err := s.workspaces.GetByID(ctx, id)
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, nil
	}
	if row.IsDefault {
		return false, errWorkspaceDefaultImmutable
	}
	if err := s.workspaces.UpdateMap(ctx, id, map[string]any{
		"path":       canonicalPath,
		"updated_at": model.NowTS(),
	}); err != nil {
		// 唯一索引撞上 = 那个目录已被另一条登记占着。**不吞**（不重读既有行）。
		if isUniqueViolation(err) {
			return false, ErrWorkspacePathTaken
		}
		return false, err
	}
	return true, nil
}

// MoveWorkspace 把 id 移到 beforeID 之前（beforeID 为空 = 追加到尾部），返回重排后的完整 id 顺序。
//
// **顺序是对的**（`0..n-1` 的一个排列），不是"尽力而为"：
//   - 一次事务里重读全部登记（不信任调用方传来的快照）；
//   - 按内存里的新顺序整段重编号——规模是"用户手动登记的项目数"（几十条），
//     区间 ±1 的优化换不来任何东西，而"重编号后逐行对账"能让任何一点漂移当场暴露；
//   - 对账不成立即回滚（行数变了 / 顺序集合不是 0..n-1 / 目标或锚点不存在）。
//
// 为什么返回**完整顺序**而不是只返回目标行：客户端的本地顺序与库的对齐靠它比对
// （对齐 dsh 的 `insertBefore` 返回完整 committed order）。
func (s *Store) MoveWorkspace(ctx context.Context, id, beforeID string) ([]string, error) {
	seq := make([]string, 0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := s.workspaceRowsTx(ctx, tx)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		at := indexOf(ids, id)
		if at < 0 {
			return errWorkspaceNotFound
		}
		without := make([]string, 0, len(ids))
		without = append(without, ids[:at]...)
		without = append(without, ids[at+1:]...)

		var next []string
		if beforeID == "" {
			next = append(without, id)
		} else {
			if beforeID == id {
				// 移到"自己前面"是空操作：直接返回当前顺序（不写库）。
				seq = ids
				return nil
			}
			bi := indexOf(without, beforeID)
			if bi < 0 {
				return errWorkspaceNotFound
			}
			next = make([]string, 0, len(ids))
			next = append(next, without[:bi]...)
			next = append(next, id)
			next = append(next, without[bi:]...)
		}

		for i, wsID := range next {
			if err := s.workspaces.WithTx(tx).UpdateMap(ctx, wsID, map[string]any{
				"list_order": int64(i),
				"updated_at": model.NowTS(),
			}); err != nil {
				return err
			}
		}
		// 对账：重编号之后，库里读到的东西必须**正好**是 next。
		if err := s.reconcileOrder(ctx, tx, next); err != nil {
			return err
		}
		seq = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	return seq, nil
}

// reconcileOrder 复核重排结果：行数不变、顺序集合恰好是 0..n-1、逐位 id 相符。
//
// 这一步刻意放在**同事务内**：任何一点不符都回滚，库不会留下一个"顺序有洞"的状态
// ——那种状态下次重排时会自己漂移，而且没有任何界面能显示它。
func (s *Store) reconcileOrder(ctx context.Context, tx *gorm.DB, want []string) error {
	got, err := s.workspaceRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return fmt.Errorf("store: 工作空间重排对账失败：行数 %d ≠ 期望 %d", len(got), len(want))
	}
	for i, r := range got {
		if r.ListOrder != int64(i) {
			return fmt.Errorf("store: 工作空间重排对账失败：位置 %d 的 list_order = %d（期望 %d）", i, r.ListOrder, i)
		}
		if r.ID != want[i] {
			return fmt.Errorf("store: 工作空间重排对账失败：位置 %d 是 %s（期望 %s）", i, r.ID, want[i])
		}
	}
	return nil
}

// CountWorkspacesByID 返回各工作空间名下的会话数（含归档；id → 计数）。
//
// 不含默认工作空间的归属：`workspace_id = ”` 的历史会话在**读面**归默认工作空间
// （设计 ADR-6），计数由 manager 那边把默认行加上 `”` 的桶得到——这里只给事实。
func (s *Store) CountWorkspacesByID(ctx context.Context) (map[string]int64, error) {
	type row struct {
		WorkspaceID string `gorm:"column:workspace_id"`
		N           int64  `gorm:"column:n"`
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&model.SessionEntity{}).
		Select("workspace_id AS workspace_id, COUNT(*) AS n").
		Group("workspace_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.WorkspaceID] = r.N
	}
	return out, nil
}

// LastUsedByWorkspaceID 返回各工作空间的最近使用时刻（id → RFC3339Nano 文本；无会话 → 不出现）。
func (s *Store) LastUsedByWorkspaceID(ctx context.Context) (map[string]string, error) {
	type row struct {
		WorkspaceID string `gorm:"column:workspace_id"`
		Last        string `gorm:"column:last"`
	}
	var rows []row
	err := s.db.WithContext(ctx).Model(&model.SessionEntity{}).
		Select("workspace_id AS workspace_id, MAX(updated_at) AS last").
		Group("workspace_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.Last != "" {
			out[r.WorkspaceID] = r.Last
		}
	}
	return out, nil
}

// AssignSessionWorkspace 设定一条会话的归属（建会话 / 迁移路径用）。
func (s *Store) AssignSessionWorkspace(ctx context.Context, sessionID, workspaceID string) error {
	return s.db.WithContext(ctx).Model(&model.SessionEntity{}).
		Where("id = ?", sessionID).
		Update("workspace_id", workspaceID).Error
}

// errWorkspaceNotFound 是"登记不存在"的库内信号（由 manager 翻成 workspace_not_found → 40401）。
var errWorkspaceNotFound = errors.New("store: workspace not found")

// errWorkspaceDefaultImmutable 是"默认工作空间不可删"的库内信号（→ 40001）。
var errWorkspaceDefaultImmutable = errors.New("store: default workspace is immutable")

// ErrWorkspaceNotFound / ErrWorkspaceDefaultImmutable 供 manager 判定（见上两条）。
var (
	ErrWorkspaceNotFound         = errWorkspaceNotFound
	ErrWorkspaceDefaultImmutable = errWorkspaceDefaultImmutable
)

// isUniqueViolation 判定错误是否为唯一索引冲突。
//
// 不按驱动错误码判（golib 的 SQLite driver 换过三代：mattn/go-sqlite3 的 CGO 方言 →
// v1.32.22 起的 ncruces 纯 Go 方言，错误码与文本都随版本变），
// 按"文本里出现 UNIQUE"判：本仓只用它做**重读既有行**这一个动作，判错的代价是丢掉一次
// 优化（重读仍然会走一遍），不是写坏数据——所以宁可宽一点。
//
// 但它**必须认得出来**：get-or-create 在并发下的正确性全压在"唯一索引报错 → 重读"这条路上。
// 若驱动换了措辞而它认不出，两条并发创建里的输家会把这行错误直接抛给用户——表现为
// "偶尔创建失败一次，再点一下就好"。所以 `TestWorkspaceUniqueIndexEnforced` 直接**注入**
// 一次冲突并断言它认得出，而不是只断言"最后库里只有一行"（后者在无并发时恒真）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "UNIQUE") || strings.Contains(msg, "CONSTRAINT FAILED")
}

// indexOf 返回 s 中 v 的位置（不存在 → -1）。
func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// SetWorkspaceDefaults 改写一个工作空间的两个默认值（`”` = 清掉该档）。
//
// **它只 UPDATE `workspaces` 一行**——绝不连带改 `sessions`。既有会话的能力集与权限模式是
// **快照**：改了工作空间默认值就把它们悄悄改掉，表现是「我调了个默认值，昨天那个会话的行为变了」。
// 这条由 archtest 的 `TestWorkspaceDefaultsDoNotRewriteSessions` 钉住。
//
// 返回 false = 该 id 不存在（不报错：调用方按 404 处理，与目录当前态同一口径）。
//
// 保留手写 UPDATE（而不是 `UpdateMap`）：调用方要区分"改到了那一行"与"那一行不存在"，
// 而 `UpdateMap` 不返回 RowsAffected。
func (s *Store) SetWorkspaceDefaults(ctx context.Context, id, capabilityJSON, permissionMode string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&model.WorkspaceEntity{}).Where("id = ?", id).
		Updates(map[string]any{
			"default_capability_json": capabilityJSON,
			"default_permission_mode": permissionMode,
			"updated_at":              model.NowTS(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// workspaceItemOf 把一张 `workspaces` 行翻成领域值。
//
// 映射只有一处：领域端口（`workspace.WorkspaceStore` / `workspace.WorkspaceTx`）只认
// `row.Item`，行形状不再越过适配器边界。改列时改这里，而不是改域。
func workspaceItemOf(r model.WorkspaceEntity) wkrow.Item {
	return wkrow.Item{
		ID:                    r.ID,
		Path:                  r.Path,
		Title:                 r.Title,
		IsDefault:             r.IsDefault,
		DefaultCapabilityJSON: r.DefaultCapabilityJSON,
		DefaultPermissionMode: r.DefaultPermissionMode,
		ListOrder:             r.ListOrder,
		CreatedAt:             r.CreatedAt,
		UpdatedAt:             r.UpdatedAt,
	}
}

// workspaceItemPtr 行指针 → 领域值指针（行不存在时保持 nil）。
func workspaceItemPtr(r *model.WorkspaceEntity) *wkrow.Item {
	if r == nil {
		return nil
	}
	it := workspaceItemOf(*r)
	return &it
}

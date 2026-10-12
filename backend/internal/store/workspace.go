package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morehao/ark-mate/internal/model"
	"github.com/morehao/ark-mate/pkg/object/objid"
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
// 单表读写仍在 `internal/dao/workspace.go`；本包不自建 SQL 拼接之外的东西。

// 默认工作空间的语言中立标题。
//
// 它存的是**这个串**而不是「默认工作空间」：一次安装跨语言切换时库里的值不变，只有屏幕上
// 的字跟着变（前端按 `is_default` 渲染本地化的名字）。这跟 dsh 的
// `DEFAULT_WORKSPACE_DIRECTORY = 'default-workspace'` 是同一手法。
const DefaultWorkspaceTitle = "default-workspace"

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
//   - **可关闭**：provision.Dir 为空（代建关闭）→ 不建立，返回 nil。这不是错误：该类部署
//     强制显式传 cwd（既有的 `workspace_required`）。
//
// **建目录是文件系统副作用，事务回滚不了它**：目录先建、再写库；写库失败时目录留在磁盘上
// （空目录，无害），由 v14 的迁移说明与 doctor 如实登记，不假装能回滚。
func (s *Store) BootstrapDefaultWorkspace(ctx context.Context, provision WorkspaceProvision) (*model.WorkspaceEntity, error) {
	dir := strings.TrimSpace(provision.Dir)
	if dir == "" {
		return nil, nil
	}
	n, err := s.workspaces.Count(ctx)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		// 表非空：可能是已有安装（用户在跑别的项目），也可能是默认被删过。
		// 两种情况都不自动创建——这正是"删了就不再回来"的实现点。
		return s.workspaces.GetDefault(ctx)
	}
	return s.createDefaultWorkspace(ctx, dir)
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
		Title:     DefaultWorkspaceTitle,
		IsDefault: true,
		ListOrder: 0,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.workspaces.Insert(ctx, &row); err != nil {
		// 并发/竞态：另一个进程刚建过 → 返回既有那一条（与 get-or-create 同一手法）。
		if isUniqueViolation(err) {
			if existing, gerr := s.workspaces.GetDefault(ctx); gerr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("store: 登记默认工作空间: %w", err)
	}
	return &row, nil
}

// DefaultWorkspace 取默认工作空间（不存在 → (nil, false, nil)）。
func (s *Store) DefaultWorkspace(ctx context.Context) (model.WorkspaceEntity, bool, error) {
	row, err := s.workspaces.GetDefault(ctx)
	if err != nil || row == nil {
		return model.WorkspaceEntity{}, false, err
	}
	return *row, true, nil
}

// ListWorkspaces 按展示顺序列出全部登记。
func (s *Store) ListWorkspaces(ctx context.Context) ([]model.WorkspaceEntity, error) {
	rows, err := s.workspaces.ListByOrder(ctx)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// GetWorkspace 按 id 取一条登记（不存在 → (nil, false, nil)）。
func (s *Store) GetWorkspace(ctx context.Context, id string) (model.WorkspaceEntity, bool, error) {
	row, err := s.workspaces.GetByID(ctx, id)
	if err != nil || row == nil {
		return model.WorkspaceEntity{}, false, err
	}
	return *row, true, nil
}

// WorkspaceByPath 按规范化路径取一条登记（不存在 → (nil, false, nil)）。
func (s *Store) WorkspaceByPath(ctx context.Context, path string) (model.WorkspaceEntity, bool, error) {
	row, err := s.workspaces.GetByPath(ctx, path)
	if err != nil || row == nil {
		return model.WorkspaceEntity{}, false, err
	}
	return *row, true, nil
}

// CreateWorkspace 登记一个工作空间（**get-or-create**：同路径已登记 → 返回既有那一行）。
//
// 为什么默认是 get-or-create 而不是报错：前端的"选一个目录建工作空间"这一步会被重复触发
// （用户多按一次、或「最近使用（未登记）」那一栏点两下），报错会让一次幂等意图看起来像失败。
// 需要"必须新建"的调用方传 distinctPath 检查（见 manager 的 strict 分支）。
func (s *Store) CreateWorkspace(ctx context.Context, path, title string) (model.WorkspaceEntity, error) {
	canonical := strings.TrimSpace(path)
	if canonical == "" {
		return model.WorkspaceEntity{}, errors.New("store: 工作空间路径不能为空")
	}
	if existing, err := s.workspaces.GetByPath(ctx, canonical); err != nil {
		return model.WorkspaceEntity{}, err
	} else if existing != nil {
		return *existing, nil
	}
	max, err := s.workspaces.MaxOrder(ctx)
	if err != nil {
		return model.WorkspaceEntity{}, err
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
			if existing, gerr := s.workspaces.GetByPath(ctx, canonical); gerr == nil && existing != nil {
				return *existing, nil
			}
		}
		return model.WorkspaceEntity{}, fmt.Errorf("store: 登记工作空间 %s: %w", canonical, err)
	}
	return row, nil
}

// RenameWorkspace 改写展示名（title 为空串 = 清空，回落到末级目录名渲染）。
func (s *Store) RenameWorkspace(ctx context.Context, id, title string) error {
	return s.workspaces.UpdateTitle(ctx, id, strings.TrimSpace(title))
}

// DeleteWorkspace 删除一条登记，并把它名下会话的归属清空（**不删会话**，设计 ADR-6）。
//
// 返回被释放的会话数。整个动作在一个事务里：登记行与归属列必须一起变，否则中断会留下
// "登记没了、会话还指着不存在的 id"——那种行在界面上表现为会话凭空消失。
//
// **默认工作空间不可删**：它是"建会话未指定 cwd"这条路的落点，删掉等于让那条路无路可走。
// 这条判定在这里（而不只在 manager）兜底：任何调用方都不该能删出一个没有落点的库。
func (s *Store) DeleteWorkspace(ctx context.Context, id string) (int64, error) {
	var released int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := s.workspaces.WithTx(tx).GetByID(ctx, id)
		if err != nil {
			return err
		}
		if row == nil {
			return errWorkspaceNotFound
		}
		if row.IsDefault {
			return errWorkspaceDefaultImmutable
		}
		res := tx.Model(&model.SessionEntity{}).Where("workspace_id = ?", id).
			Updates(map[string]any{"workspace_id": ""})
		if res.Error != nil {
			return res.Error
		}
		released = res.RowsAffected
		return tx.Delete(&model.WorkspaceEntity{}, "id = ?", id).Error
	})
	if err != nil {
		return 0, err
	}
	return released, nil
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
		txDao := s.workspaces.WithTx(tx)
		rows, err := txDao.ListByOrder(ctx)
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
			if err := txDao.SetOrder(ctx, wsID, int64(i)); err != nil {
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
	got, err := s.workspaces.WithTx(tx).ListByOrder(ctx)
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

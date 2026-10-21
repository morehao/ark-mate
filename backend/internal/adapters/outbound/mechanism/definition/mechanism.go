// Package definition 承载**五类定义**（skill / mcp / automation / provider / model）共用的机制：
// 双载体读写（库行 ↔ 文件）、版本 CAS（compare-and-swap：改之前先对 rev）、以及写冲突的
// 显式报错；导入/导出的框架与逐类适配器的公共部分也在这里。
//
// 它**负责**：载体选择（有库走库、无库走文件）、CAS 与冲突判定、"谁改的"审计、定义 id 的
// 文件名安全边界、以及定义形状的历史迁移（旧 `sources[]` / legacy mcp ref → 新形状）。
//
// 它**不负责**：某一种定义**该怎么生效**（那是各领域的事：skill 的投影与启停、mcp 的注入条目、
// automation 的调度、provider 的密钥）、对外 DTO 与错误码（`service/svc*` 与 `dto/*`）。
//
// 为什么它单独成包：五类定义共用同一套双载体 + CAS + 审计，本来就只有一份实现；它被 ≥3 个域
// 依赖，而按设计它**不依赖任何域**（`mechanism/*` 不得向上 import）。
package definition

import (
	"sync"

	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
)

// Mechanism 定义机制实例：持有载体所需的库句柄与（memory 模式的）定义目录。
//
// 并发语义与拆分前**逐字一致**：`capabilityDir` 由 `mu` 保护（写点都在装配期，读点
// `Dir()` / `capabilityDirFor()` 在运行期）；`st` 与拆分前一样只在装配期写入、运行期只读，
// 因此不加锁——这不是新引入的宽松，是**保持原样**，改它属于另一次并发审计。
type Mechanism struct {
	mu            sync.Mutex
	st            *store.Store
	capabilityDir string
}

// New 造一个未装配的定义机制（库句柄与目录随后由装配期登记）。
func New() *Mechanism { return &Mechanism{} }

// SetStore 装配期登记库句柄（与 `Service.EnableStore` 同步调用）。
func (m *Mechanism) SetStore(st *store.Store) { m.st = st }

// SetDir 装配期登记定义目录。**语义按持久层模式分叉**：memory 模式下是启停写回位置，
// sqlite 模式下只作导入/导出目录。
func (m *Mechanism) SetDir(dir string) {
	m.mu.Lock()
	m.capabilityDir = dir
	m.mu.Unlock()
}

// Dir 当前定义目录（未配置 → 空串）。
func (m *Mechanism) Dir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.capabilityDir
}

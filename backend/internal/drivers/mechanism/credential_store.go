package mechanism

import (
	"strings"

	"github.com/morehao/ark-mate/internal/infra/credentials"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/runtime"
)

// Store 是凭据机制持有的**状态**：四层凭据面与凭据 cwd。
//
// 并发语义与拆分前**逐字一致**：两者都是**装配期写一次、之后只读**，所以不加锁——
// 这不是新引入的宽松，是保持原样。为什么不加锁：读它的调用点在请求路径上被并发走到，
// 而其中一些已经持着 `Service.mu`（读模型）；再给它配一把锁只会制造锁序问题。
type Store struct {
	layered *credentials.Layered
	cwd     string
	st      *store.Store
	reg     *runtime.Registry
}

// New 造一个凭据 Store。runtime 注册表由构造期给出（清单面，装配期即固定）；
// 四层凭据面与库句柄随后由 `SetLayered` / `SetStore` 登记。
func NewCredentialStore(reg *runtime.Registry) *Store { return &Store{reg: reg} }

// SetLayered 登记四层凭据面（**装配期写一次、之后只读**）。
func (m *Store) SetLayered(l *credentials.Layered) { m.layered = l }

// Layered 已登记的四层凭据面（未装配 → nil）。
func (m *Store) Layered() *credentials.Layered { return m.layered }

// SetStore 装配期登记库句柄（与 `Service.EnableStore` 同步调用）。
func (m *Store) SetStore(st *store.Store) { m.st = st }

// SetCwd 登记**凭据 cwd**（`<cwd>/.env` 那一层的 cwd = 用户启动 `mate serve` 的目录）。
//
// 必须**显式登记**而不是用到时 `os.Getwd()`：这个值要随网关条目交给网关**子进程**
// （见 `mcp.GatewayCredCwdEnv`），而网关的 cwd 是底座给的（会话工作区）——各自取自己的 cwd，
// 同一个引用就会在两个进程里解析出不同的值，且完全静默。
func (m *Store) SetCwd(dir string) { m.cwd = strings.TrimSpace(dir) }

// Cwd 登记过的凭据 cwd（空 = 装配期没登记）。
func (m *Store) Cwd() string { return m.cwd }

// Doc 托管镜像句柄（未装配 → nil）。
func (m *Store) Doc() *credentials.Doc {
	if m.layered == nil {
		return nil
	}
	return m.layered.Managed
}

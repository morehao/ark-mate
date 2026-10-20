package credential

// Source 是"这把钥匙从哪一层来的"——它要出现在面板与 `mate doctor` 上，所以取值是**对外词表**，
// 不能随手改（改了等于让前端与文档同时漂移）。刻意用下划线形态：它进 JSON 字段，不进 Go 标识符。
//
// 为什么归在这一层（L1 值层）而不是 `internal/infra/credentials`：它是**凭据的词汇**，不是
// 某一套解析实现的细节——领域层的快照字段（`provider.Snapshot.KeySource`）与用例层的诊断分支
// 都要用它。留在 infra 里会让 `domain` 与 `service` 为了一个枚举反向依赖基础设施。
type Source string

const (
	// SourceEnv 继承的进程环境（只读，最高优先级）。
	SourceEnv Source = "env"
	// SourceManaged 托管文档（可写；面板/CLI 写的密钥落在这里）。
	SourceManaged Source = "managed"
	// SourceDotenvCwd 调用目录下的 `.env`（只读）。
	SourceDotenvCwd Source = "dotenv_cwd"
	// SourceDotenvHome 数据目录下的 `.env`（只读）。
	SourceDotenvHome Source = "dotenv_home"
)

// Writable 报告这一层能不能被平台写：只有托管文档能。面板要据此决定"这条密钥是只读继承来的、
// 还是能在界面上改的"——把继承来的密钥显示成可编辑，用户改完发现不生效（下次启动又被环境变量顶掉）。
func (s Source) Writable() bool { return s == SourceManaged }

// Package attachmentstore 是附件字节存储的薄适配层：把 golib `filestore`（+ `storage/driver/local`）
// 收在一个端口后面，实现 `domain/attachment` 的解析端口，供装配根注给用例。
//
// 为什么单独成包（而不是让 svc/sessionruntime 直接用 filestore）：
//   - **字节落盘去重复用 golib**（设计 §3.1）：`UploadAndRecord` 边写边算 SHA-256 并按哈希去重、
//     `Open(id)` 回流、`DeleteFile(id)` 删记录且按去重规则回收 blob——这些都不该平台自己造；
//   - **平台语义不外泄**：会话归属 / kind / state / 生命周期在 `attachments` 表（`persistence/store`），
//     本包只管「字节这一层的读写」，不碰业务状态机；
//   - 本地驱动 `BaseDir=<MATE_HOME>/attachments`：文件只落平台自己的数据目录，**绝不写用户工作区**（§5.3.2）。
package attachmentstore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/morehao/golib/filestore"
	"github.com/morehao/golib/storage"
	// local 驱动经 init() 注册到 storage 注册表（New 按 driver 名查表）。
	_ "github.com/morehao/golib/storage/driver/local"
	"gorm.io/gorm"
)

// bucket 固定桶名（local 驱动下就是 BaseDir 下的一个子目录段）。
const bucket = "ark-mate"

// Store 是附件字节存储的薄适配层，内部持一个 *filestore.FileStore。
type Store struct {
	fs      *filestore.FileStore
	baseDir string
}

// FileStore 暴露底层的 golib filestore，供装配根把**唯一的上传 HTTP 面**（`ginupload.Register`）
// 挂到 `/files` 上（产物溯源 P0′：全局收字节收口，见设计 §3.4）。
//
// 只读访问器：不改变本包对字节的唯一写路径（`UploadAndRecord` 等），只是把同一个 FileStore
// 实例交给 ginupload 注册路由——两处共用同一实例，因此去重表、上限、驱动配置天然一致。
func (s *Store) FileStore() *filestore.FileStore { return s.fs }

// Options 构造参数。
type Options struct {
	// BaseDir 本地驱动落盘根目录（`<MATE_HOME>/attachments`）。
	BaseDir string
	// MaxUploadBytes 单文件上限（防呆/防灌盘；<=0 表示不限，由调用方按配置给）。
	MaxUploadBytes int64
	// SignSecret 本地驱动预签名 HMAC-SHA256 密钥（空 = 预签名操作返回 ErrNotSupported）。
	SignSecret string
	// BaseURL 对外公共访问基础 URL（本地驱动拼预签名 URL 用；空 = 预签名报 ErrInvalidConfig）。
	BaseURL string
}

// New 构造附件字节存储。构造期**不建表**（`WithoutAutoMigrate`）：`core_file*` 的 DDL 由启动期
// 显式 `Migrate(db)` 执行一次（设计 §3.2 的 AutoMigrate 例外，登记在 build-and-runtime.md）。
//
// 建 local 驱动需要先补齐 BaseDir；`local.New` 内部会 MkdirAll(0755)。
func New(db *gorm.DB, opts Options) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("attachmentstore.New: db is required")
	}
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("attachmentstore.New: BaseDir is required")
	}
	st, err := storage.New(storage.DriverLocal, storage.Config{
		BaseDir:    filepath.Clean(opts.BaseDir),
		SignSecret: opts.SignSecret,
		BaseURL:    opts.BaseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("attachmentstore.New: build local driver: %w", err)
	}
	fs, err := filestore.New(db, st, bucket,
		filestore.WithoutAutoMigrate(),
		filestore.WithMaxUploadBytes(opts.MaxUploadBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("attachmentstore.New: build filestore: %w", err)
	}
	return &Store{fs: fs, baseDir: filepath.Clean(opts.BaseDir)}, nil
}

// Migrate 建/补齐 `core_file`、`core_file_upload` 两表（幂等）。启动期在自管迁移之后调一次。
//
// 这是本仓「不用 gorm AutoMigrate」纪律的**唯一**例外（且只对 core_file* 两张 golib 组件表），
// 事实源在 golib；已登记进 docs/agents/build-and-runtime.md。
func Migrate(db *gorm.DB) error {
	return filestore.Migrate(db)
}

// UploadAndRecordRequest 是「把一段字节写入存储并留一条上传记录」的入参。
//
// ContentHash 若形如 64 位十六进制，filestore 会校验它与实测一致（防污染去重表）。
type UploadAndRecordRequest struct {
	ContentHash string
	Name        string
	Size        int64
	MimeType    string
	Reader      io.Reader
	// Scene 弱分组标记（filestore 侧审计便利；会话归属的强语义在 attachments.session_id）。
	Scene string
}

// UploadAndRecord 写入字节并留一条上传记录，返回 filestore 的 FileDetail（含 FileUploadID / ContentHash / Size）。
func (s *Store) UploadAndRecord(ctx context.Context, req UploadAndRecordRequest) (*filestore.FileDetail, error) {
	return s.fs.UploadAndRecord(ctx, filestore.UploadAndRecordRequest{
		ContentHash: req.ContentHash,
		Name:        req.Name,
		Size:        req.Size,
		MimeType:    req.MimeType,
		Reader:      req.Reader,
		Scene:       req.Scene,
	})
}

// Open 按 filestore upload id（= attachments.upload_id）回流字节流与元数据。
func (s *Store) Open(ctx context.Context, uploadID string) (io.ReadCloser, *filestore.FileDetail, error) {
	return s.fs.Open(ctx, uploadID)
}

// GetFile 按 upload id 取元数据（doctor 体检 / 反查用）。
func (s *Store) GetFile(ctx context.Context, uploadID string) (*filestore.FileDetail, error) {
	return s.fs.GetFile(ctx, uploadID)
}

// DeleteFile 删一条上传记录（物理 blob 是否真删由 filestore 内容去重规则决定）。
func (s *Store) DeleteFile(ctx context.Context, uploadID string) error {
	return s.fs.DeleteFile(ctx, uploadID)
}

// ReadPath 返回某 upload 的 blob 在本地磁盘上的**绝对只读路径**（文档/大图 handle 用）。
//
// 解析链：GetFile → StorageURI（本地驱动下 `file:///<bucket>/<key>`）→ 磁盘路径。
// 本地驱动的落盘布局是固定的 `<BaseDir>/data/<bucket>/<key>`（见 golib storage/driver/local
// 的 dataPath）。这里按同一布局重建——若 golib 改了布局，doctor 的 unreadable 体检与 P4 行为
// 验收会当场红（而不是静默给出错路径）。
// 注意：本方法只服务本地驱动；远程底座（未来云端）不走这条路（设计 §5.3.4）。
func (s *Store) ReadPath(ctx context.Context, uploadID string) (string, error) {
	detail, err := s.fs.GetFile(ctx, uploadID)
	if err != nil {
		return "", err
	}
	return diskPathForStorageURILocal(s.baseDir, detail.StorageURI)
}

// diskPathForStorageURILocal 把本地驱动的 StorageURI（`file:///<bucket>/<key>`）翻成磁盘绝对路径。
// 布局：`<baseDir>/data/<bucket>/<key>`（对齐 golib storage/driver/local 的 dataPath）。
func diskPathForStorageURILocal(baseDir, storageURI string) (string, error) {
	bucket, key, err := splitLocalStorageURI(storageURI)
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "data", bucket, filepath.FromSlash(key)), nil
}

// splitLocalStorageURI 解析本地驱动的 file:// URI，返回 (bucket, key)。
func splitLocalStorageURI(storageURI string) (bucket, key string, err error) {
	_, bucket, key, err = storage.ParseURI(storageURI)
	return bucket, key, err
}

// BlobStat 描述一条 upload 在磁盘上的可达性（doctor 只读体检用）。
type BlobStat struct {
	// UploadRowFound core_file_upload 里有没有这个 upload_id 的行。
	UploadRowFound bool
	// StorageURI 该 upload 指向的物理文件 URI（UploadRowFound 时有效）。
	StorageURI string
	// FileFound blob 文件在磁盘上是否存在（UploadRowFound 且 URI 可解析时有效）。
	FileFound bool
	// Error 采集失败的原因（nil = 采集顺利）。
	Error error
}

// InspectBlob 只读地检查一条 upload 在磁盘上的可达性：查 core_file_upload / core_file（只 SELECT）
// + os.Stat blob。**不建表、不写盘、不构造驱动**——doctor 以 mode=ro 打开库时也必须能跑。
//
// baseDir = `<MATE_HOME>/attachments`（与运行期 New 一致），用于把 StorageURI 翻成磁盘路径。
func InspectBlob(ctx context.Context, db *gorm.DB, baseDir, uploadID string) BlobStat {
	var out BlobStat
	if db == nil {
		out.Error = fmt.Errorf("attachmentstore.InspectBlob: db is nil")
		return out
	}
	// core_file_upload.file_id → core_file.storage_uri。
	var row struct {
		FileID string
	}
	res := db.WithContext(ctx).Raw(
		`SELECT file_id FROM core_file_upload WHERE id = ?`, uploadID).Scan(&row)
	if res.Error != nil {
		out.Error = res.Error
		return out
	}
	if row.FileID == "" {
		return out // UploadRowFound=false：附件行悬挂（missing）
	}
	out.UploadRowFound = true

	var file struct {
		StorageURI string
	}
	res = db.WithContext(ctx).Raw(
		`SELECT storage_uri FROM core_file WHERE id = ?`, row.FileID).Scan(&file)
	if res.Error != nil {
		out.Error = res.Error
		return out
	}
	out.StorageURI = file.StorageURI
	if file.StorageURI == "" {
		return out
	}
	diskPath, err := diskPathForStorageURILocal(baseDir, file.StorageURI)
	if err != nil {
		out.Error = err
		return out
	}
	if st, statErr := os.Stat(diskPath); statErr == nil && !st.IsDir() {
		out.FileFound = true
	}
	return out
}

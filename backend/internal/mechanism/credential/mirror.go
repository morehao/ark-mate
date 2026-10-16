package credential

import (
	"context"
	"strings"

	"github.com/morehao/ark-mate/pkg/credentials"
	"github.com/morehao/golib/glog"
)

// 托管镜像里键的增删查。**参数化**：文档由调用方传入（`*Service` 那边经一行转发把
// `credentialDoc()` 交进来），所以这三段与编排层无关，可以独立搬进机制层。
//
// doc == nil 时的一致口径：读 → 未命中；写/删 → 静默成功（`nil` 表示"没有配托管镜像"，
// 此时按 `env:` 走本来就是正常形态，不该报错）。

// MirrorValue 读托管镜像里的一条引用（未装配/读不了 → 空）。
func MirrorValue(doc *credentials.Doc, ref string) (string, bool) {
	if doc == nil || strings.TrimSpace(ref) == "" {
		return "", false
	}
	values, err := doc.Load()
	if err != nil {
		return "", false
	}
	v, ok := values[ref]
	return v, ok
}

// MirrorSet / mirrorDelete 库写完之后的**双写**（失败只 WARN，由调用方决定要不要进 problems）。
func MirrorSet(doc *credentials.Doc, ref, value string) error {
	if doc == nil {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return MirrorDelete(doc, ref)
	}
	if err := doc.Set(ref, value); err != nil {
		glog.Warnw(context.Background(),
			"credentials: 托管镜像写入失败（库已写成功；镜像可由库重建，不阻断也不补偿）",
			"ref", ref, "err", err)
		return err
	}
	return nil
}

func MirrorDelete(doc *credentials.Doc, ref string) error {
	if doc == nil {
		return nil
	}
	if err := doc.Delete(ref); err != nil {
		glog.Warnw(context.Background(),
			"credentials: 托管镜像删除失败（库已改成功；镜像可由库重建，不阻断也不补偿）",
			"ref", ref, "err", err)
		return err
	}
	return nil
}

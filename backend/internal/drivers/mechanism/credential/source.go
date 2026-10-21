package credential

import (
	"context"
	"errors"
	"strings"

	credval "github.com/morehao/ark-mate/internal/platform/credential"

	"github.com/morehao/ark-mate/internal/infra/credentials"
	"github.com/morehao/ark-mate/internal/adapters/outbound/sqlite/store"
	"github.com/morehao/golib/glog"
)

// 本文件是凭据机制**对外的装配与入口面**：托管镜像在哪、四层凭据面怎么装、镜像怎么对账。
// 全部是**参数化**的（库句柄与 home 目录都由调用方传入），与 `*Service` 无关——这也是它们
// 能先搬、不必等 `credentials.go` 主体那 12 个方法的原因。

// OpenCredentialSource 按约定装配四层凭据面：`<homeDir>/credentials.json` +
// `<cwdDir>/.env` + `<homeDir>/.env`。返回的第二个值是镜像路径（诊断面要打印它）。
//
// 打开镜像**不读内容**（`credentials.Open` 只登记路径）：读动作发生在每一次解析上，而权限
// 过宽时那次读会硬失败——"权限不合 fail-loud"因此落在真正读它的地方，而不是伪装成装配失败。
func OpenCredentialSource(homeDir, cwdDir string) (*credentials.Layered, string, error) {
	path := credval.CredentialsPath(homeDir)
	if path == "" {
		return nil, "", errors.New("数据目录为空（MATE_HOME 未解析出来）：托管凭据文档没有落点")
	}
	doc, err := credentials.Open(path)
	if err != nil {
		return nil, "", err
	}
	return credentials.New(doc, cwdDir, homeDir), path, nil
}

// SyncCredentialMirrorFromStore 包级版本：给**不走 Service** 的调用方用（`mate provider import`
// 与 `mate capability import` 这两条 CLI 路径各自开库，没有装配出来的 Service）。
//
// homeDir 是镜像的锚点（`<MATE_HOME>/credentials.json`），**不是** ProviderDir 的父目录——
// v20 起凭据与档案目录无关，只有旧机制的遗留物还按后者取（见 provider_doctor.go）。
func SyncCredentialMirrorFromStore(ctx context.Context, st *store.Store, homeDir, reason string) (int, error) {
	if st == nil || strings.TrimSpace(homeDir) == "" {
		return 0, nil
	}
	doc, err := credentials.Open(credval.CredentialsPath(homeDir))
	if err != nil {
		return 0, err
	}
	return SyncMirror(ctx, st, doc, reason)
}

// SyncMirror 合并实现（两个入口共用：一处实现，两条调用路径不可能分叉）。
func SyncMirror(ctx context.Context, st *store.Store, doc *credentials.Doc, reason string) (int, error) {
	values, err := doc.Load()
	if err != nil {
		return 0, err
	}
	changed := 0
	put := func(ref, value string) {
		if strings.TrimSpace(ref) == "" || strings.TrimSpace(value) == "" {
			return
		}
		if cur, ok := values[ref]; ok && cur == value {
			return
		}
		values[ref] = value
		changed++
	}
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil {
		return changed, err
	}
	for i := range rows {
		if strings.TrimSpace(rows[i].Key) == "" {
			continue // 空串 = 没有密钥，镜像里不该有这一条（存量条目由 doctor 报出，不在这里删）
		}
		put(credval.ProviderRef(rows[i].ID), rows[i].Key)
	}
	secrets, err := st.ListMcpSecrets(ctx)
	if err != nil {
		return changed, err
	}
	for i := range secrets {
		put(secrets[i].Ref, secrets[i].Value)
	}
	if changed == 0 {
		return 0, nil
	}
	if err := doc.Save(values); err != nil {
		return 0, err
	}
	glog.Infow(ctx, "credentials: 托管镜像已按库对账（库是权威，镜像可重建）",
		"changed", changed, "total", len(values), "reason", reason)
	return changed, nil
}

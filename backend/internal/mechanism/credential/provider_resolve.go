// 本文件是 provider 拨号密钥的解析入口（provider 面唯一入口）。
package credential

import (
	"context"
	"github.com/morehao/ark-mate/internal/credential"
	credval "github.com/morehao/ark-mate/internal/credential"
	"strings"

	"github.com/morehao/ark-mate/internal/infra/credentials"
)

// resolveProviderKey 解析一条 provider 的拨号密钥（provider 面**唯一**入口）。
//
// 四层顺序由 `pkg/credentials.Layered` 一处实现；这里只做一件它做不到的事——**把"库"并进
// 托管那一层**，因为库才是权威、镜像只是副本：
//
//   - 分层命中 `managed`（镜像里有）时，与库对一次账：**库里有值才采用**（两边都有时以库为准）。
//     镜像里有、库里没有 = 镜像陈旧或曾写入失败——**不采用**，否则"在面板上清掉的密钥"会被
//     一份陈旧镜像悄悄撤销（那是把一次删除反转成静默的 no-op）。这条只在**否决**一个命中，
//     不改变层与层的先后，所以顺序实现仍然只有一处。
//   - 分层什么都没命中、也没有"读不了"的问题时，库里那一份**仍然可用**（镜像丢了不丢密钥）。
//   - 分层报"某层读不了"（镜像权限过宽 / 形状坏掉 / `.env` 写坏了）时，问题原样带出去；
//     只要库里那一份在，密钥就还是可用的——权威在库，"镜像坏了"不等于"密钥没了"。
//
// 没有库（memory 模式）时托管层就是镜像文档本身：文件即事实源。
func (m *Store) ResolveProviderKey(ctx context.Context, id, keyRef string) credval.ProviderKey {
	envKeys := m.ProviderEnvKeyNames(id)
	req := credentials.Request{Ref: keyRef, EnvKeys: envKeys}
	var res credentials.Resolution
	if m.Layered() == nil {
		res = credentials.Resolution{Problems: []string{"凭据分层面未装配：只有库里的那一份可用"}}
	} else {
		res = m.Layered().Resolve(req)
	}
	out := credval.ProviderKey{Value: res.Value, Source: res.Source, Problems: res.Problems}
	if out.Source == credential.SourceEnv {
		out.EnvKey = m.MatchEnvKey(envKeys, out.Value)
	}
	if m.st == nil || strings.TrimSpace(id) == "" {
		return out
	}
	dbKey, found, err := m.st.ProviderKey(ctx, id)
	if err != nil {
		out.Problems = append(out.Problems, "读库里的密钥失败："+err.Error())
		return out
	}
	if !found {
		// 行不在了（装载与写面之间的窗口）：不采用镜像里那一份。
		if out.Value != "" {
			return credval.ProviderKey{Problems: []string{
				"库里没有这条供应商了（可能刚被删掉）：不采用托管文档里那一份"}}
		}
		return out
	}
	switch {
	case out.Source == credential.SourceManaged && strings.TrimSpace(dbKey) == "":
		return credval.ProviderKey{Problems: []string{
			"托管文档里有这条密钥而库里没有（镜像陈旧，或写入/删除曾失败）：**不采用**镜像里那一份" +
				"（否则在面板上清掉的密钥会被它悄悄撤销）；在「设置 · 模型」里重设一次即可对齐"}}
	case out.Source == credential.SourceManaged:
		out.Value = dbKey // 两边都有时以库为准。
		return out
	case out.Value == "" && strings.TrimSpace(dbKey) != "":
		return credval.ProviderKey{Value: dbKey, Source: credential.SourceManaged, Problems: out.Problems}
	}
	return out
}

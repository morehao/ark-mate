package assembly

import (
	"context"

	"github.com/morehao/ark-mate/internal/platform/credential"
	"github.com/morehao/golib/glog"
)

// credentials 解析本次进程的凭据表（M1-1）。
//
// 两个来源，优先级明确：
//  1. **命令层注入**（桌面态 `--auth-stdin` 的双凭据）：显式来源，最高优先级；
//  2. **MATE_TOKEN**（独立态历史行为）：折叠成一条同时具备 rest 与 stream 两个面的凭据，
//     因此"同一个令牌既能调 REST 也能订阅事件流"的既有语义逐字保留。
//
// 两者同时存在时以注入为准并告警：桌面态的核心由壳 spawn，用户 shell 里残留的
// MATE_TOKEN 不该改变桌面态的凭据分层（它只该管独立态）。
func (as *assembly) credentials() *credential.Set {
	if as.opts.Credentials.Required() {
		if as.cfg.Token != "" {
			glog.Warnw(context.Background(), "serve: --auth-stdin 与 MATE_TOKEN 同时存在，以 --auth-stdin 的双凭据为准（MATE_TOKEN 被忽略）")
		}
		return as.opts.Credentials
	}
	if as.cfg.Token != "" {
		return credential.MustNew(credential.Pair{
			Token:  as.cfg.Token,
			Scopes: credential.ScopeREST | credential.ScopeStream,
		})
	}
	return nil
}

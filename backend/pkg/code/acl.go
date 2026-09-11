package code

import (
	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/golib/gerror"
)

// notFoundCodes 是 acl 层"目标不存在"这一类稳定 code。
//
// 它们落在 acl.ClassRequest 里（对上游而言确实是一次被拒的请求），但对调用方
// 而言语义是 404：再改参数也不会有结果，得先把资源建好。故单独提档到 NotFound，
// 让前端能按 40401 直接引导"去注册 agent / 去建 runtime"，而不是当成参数错误。
var notFoundCodes = map[string]bool{
	"agent_not_found":        true,
	"conversation_not_found": true,
	"runtime_not_found":      true,
	"mcp_not_found":          true,
	"provider_not_found":     true,
}

// fromACL 把 acl 层错误按其 Class 映射为业务码。
//
// 这张表是 acl 分类（可操作语义：重试/登录/换参数/降级）到对外码的单向投影，
// 因此**放在 code 包**而不是 acl 包：acl 是可复用的 ACP 客户端，不该知道
// ark-forge 的信封编号；反过来 code 包作为"编号唯一事实源"必须收下这张表，
// 否则同一个 acl 错误在控制器与领域层会映射成两个不同的码。
func fromACL(err error) (gerror.Error, bool) {
	ae, ok := acl.As(err)
	if !ok {
		return gerror.Error{}, false
	}
	c := UpstreamFailed
	switch ae.Class {
	case acl.ClassRequest:
		c = InvalidRequest
		if notFoundCodes[ae.Code] {
			c = NotFound
		}
	case acl.ClassLimit:
		c = Conflict
	case acl.ClassAccess:
		c = Unauthorized
	case acl.ClassUnsupported:
		c = NotSupported
	case acl.ClassService, acl.ClassConnection:
		c = BadGateway
	}
	// acl 的 Details 是脱敏后给用户看的一句话；缺省回落到稳定 code，
	// 保证 msg 永不为空（空 msg 在前端会渲染成一个没有内容的错误气泡）。
	msg := ae.Details
	if msg == "" {
		msg = ae.Code
	}
	return Error(c, msg), true
}

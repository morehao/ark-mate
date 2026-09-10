// Package svcerr 定义领域错误 → 传输层错误码的映射端口。
//
// 过渡期约束：领域实现（internal/manager）目前持有 acl 错误分类到 REST 信封码
// （40001/40401/40501/40901/50201…）的映射表，故以端口形式外露。P5 把错误码收敛到
// pkg/code 后，控制器改为直接 gincontext.Fail(ctx, err)，本端口随之删除。
package svcerr

// Mapper 把领域错误映射为携带业务错误码的 error。
type Mapper interface {
	RestErr(err error) error
}

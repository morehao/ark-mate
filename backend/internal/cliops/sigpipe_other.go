//go:build !unix

package cliops

// ignoreSIGPIPE 在非 unix 平台是空操作：那里没有 SIGPIPE 的"默认终止"语义。
func ignoreSIGPIPE() {}

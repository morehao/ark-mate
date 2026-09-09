//go:build !unix

package procreg

import (
	"fmt"
	"time"
)

// 非 unix 平台不提供进程组信号：登记与读取仍可用，回收显式降级为「不支持」。
func alive(int) bool                   { return false }
func pgidOf(int) int                   { return -1 }
func termSignal() sig                  { return 0 }
func killSignal() sig                  { return 0 }
func waitGone(int, time.Duration) bool { return true }
func gone(error) bool                  { return false }

type sig = int

func killGroup(Entry, sig) error {
	return fmt.Errorf("procreg: 当前平台不支持进程组信号（孤儿回收已降级）")
}

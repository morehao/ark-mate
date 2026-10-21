package mcpgateway

import "os"

// pidOf 当前进程号（只进诊断日志；失败给 0——日志里少一个数字远好过因此 panic）。
func pidOf() int { return os.Getpid() }

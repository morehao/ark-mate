package app

// 测试二进制里的日志 driver 注册。
//
// 本仓的日志出口归**装配根**（`cmd/mate/main.go` 的匿名引入，见 internal/bootstrap/app/command/logging.go
// 的分工说明）；而 `go test ./internal/bootstrap/app` 编译出的二进制不含 `cmd/mate`，于是
// `store.Open → dbclient.Open → dbgorm.New → glog.NewLogger` 会因为
// "unknown LoggerType zap" 直接失败。
//
// 放在**本包自己的测试文件**里而不是 `internal/adapters/outbound/sqlite/db`：后者是库包，不替调用方决定日志出口
// （这正是把匿名引入挪出 internal/adapters/outbound/sqlite/db 的初衷）。任何一个包只要自己打开库，就要自己注册。
import _ "github.com/morehao/golib/glog/driver/zap"

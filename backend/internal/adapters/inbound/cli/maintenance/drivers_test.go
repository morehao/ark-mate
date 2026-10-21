package maintenance

// 测试期注册 glog 的 zap 驱动：这些用例会真的开库（`dbclient` 建 logger 时要它）。
// 此前靠同包 `cliops/logging_test.go` 的副作用，v3.2 刀 10 搬包后必须自己声明。
import _ "github.com/morehao/golib/glog/driver/zap"

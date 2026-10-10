package mcp

// helper 子进程（TestMain 的 MATE_MCP_HELPER 分支）会按真实 `mate mcp` 的形态初始化
// glog（文件 writer），因此测试二进制需要 zap driver 注册：生产侧的注册在装配根
// （cmd/mate 的匿名引入），而这里没有 cmd/mate。
import _ "github.com/morehao/golib/glog/driver/zap"

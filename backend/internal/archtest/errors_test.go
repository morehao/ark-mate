package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// errExitCall 匹配控制器的错误出口调用，捕获实参文本。
var errExitCall = regexp.MustCompile(`gincontext\.(?:Fail|Abort)\(c,\s*([^)]*)\)`)

// TestControllerErrorExitsCarryCode 控制器的错误出口必须携带业务码。
//
// 为什么需要静态守卫：gincontext.buildErrorResponse 对**不带** gerror.Error 的错误
// 填 `code: -1`（msg 取根因）。`-1` 不是任何契约里的码，前端只能当"未知错误"兜底，
// 用户拿到的是一个无法判定的失败。P5 之前这个洞靠"服务层恰好已经映射过"堵着——
// 也就是一条隐式约定：任何一层漏映射，用户就会看到 -1，而编译器和测试都不会说话。
//
// 所以入口统一为两种写法：code.FromError(err)（归一任意 error），或
// code.Error(code.X, msg) / code.Errorf(...)（控制器自己判定的入参错误）。
// 直接透传裸 err 会在本测试红。
//
// 允许的例外：本测试只看 internal/delivery/controller/ 下**非测试**文件的 gincontext 出口。
// 其它层的裸 error 仍可由上层归一（FromError 对已带码错误原样透传，不会二次包装）。
func TestControllerErrorExitsCarryCode(t *testing.T) {
	root := backendRoot(t)
	dir := filepath.Join(root, "internal", "delivery", "controller")
	allowed := []string{"code.FromError(", "code.Error(", "code.Errorf("}

	var checked int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range errExitCall.FindAllStringSubmatch(string(raw), -1) {
			checked++
			arg := strings.TrimSpace(m[1])
			ok := false
			for _, a := range allowed {
				if strings.HasPrefix(arg, a) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("错误出口未带业务码：%s 里的 gincontext.Fail/Abort(c, %s)\n"+
					"    会被 gincontext 填成 code:-1。请改用 code.FromError(%s) "+
					"或 code.Error(code.X, …)。", rel(root, path), arg, arg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历控制器目录失败: %v", err)
	}
	// 防呆：若控制器目录被搬走或写法大改，正则一条都匹配不到，本测试会变成空转。
	if checked == 0 {
		t.Fatalf("%s 下没有匹配到任何 gincontext.Fail/Abort 出口——守卫已空转，请检查匹配规则", rel(root, dir))
	}
}

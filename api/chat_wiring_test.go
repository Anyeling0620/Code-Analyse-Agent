package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// 这三个 handler 都会在「服务层建 run」时可能拿到 SessionBusy：
// ChatCompletion 走阻塞式、ChatStream 走 SSE、ChatResume 走审批恢复。
// 它们的错误分支必须用 h.serviceErrno 透传，一旦被改回 ServerError.WithError，
// 409 就又会被压成 500，而前端只能看到"服务器错误"。
var runCreatingHandlers = []string{"ChatCompletion", "ChatStream", "ChatResume"}

// chat.goBody 取出 api/chat.go 里指定方法的源码片段。
func chatHandlerBody(t *testing.T, funcName string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "chat.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 api/chat.go 失败：%v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name == nil || fn.Name.Name != funcName {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		src, err := readFileRange("chat.go", start, end)
		if err != nil {
			t.Fatalf("读取 %s 片段失败：%v", funcName, err)
		}
		return src
	}
	t.Fatalf("api/chat.go 中找不到 handler %s", funcName)
	return ""
}

// readFileRange 读取文件 [start,end) 的字节区间。
func readFileRange(path string, start, end int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if start < 0 || end > len(data) || start > end {
		return "", fmt.Errorf("range [%d,%d) out of bounds (len=%d)", start, end, len(data))
	}
	return string(data[start:end]), nil
}

// 守卫测试：三个建 run 的 handler 必须透传业务错误码，并且走会带上 run_id 的出口。
//
// 这两件事必须一起守：只透 errno 不带 data，前端 409 快路径就拿不到 run_id
// （reviewer F7）；只改 data 而不透 errno，409 又会被压成 500。
func TestRunCreatingHandlersPropagateServiceErrno(t *testing.T) {
	for _, name := range runCreatingHandlers {
		body := chatHandlerBody(t, name)
		if !strings.Contains(body, "h.writeServiceErr(ctx, err)") {
			t.Errorf("%s 的错误分支必须调用 h.writeServiceErr(ctx, err)：它同时负责 errno 透传与 run_id data", name)
		}
		// 服务层错误若在这些 handler 里被硬包成 ServerError，等于把 409 永远吞掉。
		if strings.Contains(body, "ServerError.WithError(err)") {
			t.Errorf("%s 仍在使用 common.ServerError.WithError(err)，会把业务码统一压成 500", name)
		}
		// 直接 writeResp(..., nil, ...) 会把 SessionBusyError.RunID 丢掉，
		// 前端只能回退去多打一次 GET /api/chat/run/active。
		if strings.Contains(body, "writeResp(ctx, nil, h.serviceErrno(err))") {
			t.Errorf("%s 仍在用 writeResp(ctx, nil, serviceErrno(err))，会丢弃 SessionBusyError.RunID", name)
		}
	}
}

// 已开始写 SSE 的失败分支不能再走 writeResp（此时响应头已发出）。
// 这条守卫锁住 api/chat.go 里"流已开始就静默返回"的既有约束，
// 防止将来有人为了透错把 writeResp 加回流内分支。
func TestStreamingBranchesDoNotWriteRespAfterStreamStarts(t *testing.T) {
	for _, name := range []string{"ChatStream", "ChatResume"} {
		body := chatHandlerBody(t, name)
		// writeEvent 失败之后必须直接 return，且该分支内不得出现 h.writeResp。
		// 实际写法是 `err = writer.writeEvent(...)`（err 在前面已声明），
		// 用不带赋值符号的前缀匹配，兼容 := 与 = 两种写法。
		idx := strings.Index(body, "writer.writeEvent(")
		if idx < 0 {
			t.Fatalf("%s 中找不到 writeEvent 写 Ready 事件的分支", name)
		}
		rest := body[idx:]
		// 该分支体很短，取到下一个 return 之后为止即可判断。
		endIdx := strings.Index(rest, "h.pumpRun(")
		if endIdx < 0 {
			t.Fatalf("%s 中找不到 pumpRun 调用", name)
		}
		branch := rest[:endIdx]
		if strings.Contains(branch, "h.writeResp(") {
			t.Errorf("%s 在流已开始后仍调用 writeResp，会在已发出响应头的连接上重复写状态码", name)
		}
	}
}

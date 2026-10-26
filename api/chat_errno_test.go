package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"edu.agent.code/common"
	"edu.agent.code/service/conversation"
	"edu.agent.code/service/dto"

	"github.com/gin-gonic/gin"
)

// chatErrTestContext 构造一个最小的 gin 上下文，并预置登录用户与 trace id。
func chatErrTestContext(t *testing.T, method, target string, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	if body == "" {
		ctx.Request = httptest.NewRequest(method, target, nil)
	} else {
		ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
	}
	ctx.Set(common.CtxKeyAuthUser, &common.UserInfo{UserID: "u1"})
	ctx.Set(common.CtxKeyTraceID, "trace-test")
	return ctx, recorder
}

// decodeErrResp 解析 dto.Response，供断言 code / message。
func decodeErrResp(t *testing.T, recorder *httptest.ResponseRecorder) dto.Response {
	t.Helper()
	var resp dto.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON：%v body=%s", err, recorder.Body.String())
	}
	return resp
}

// serviceErrno 是本次改造的核心映射点：
// 业务 Errno 必须原样保留 Code，其它错误必须继续落 500。
func TestServiceErrnoPassesBusinessCodeAndKeepsServerFallback(t *testing.T) {
	h := &Handler{}

	// 生产同款错误类型：同会话已有运行中的回答。
	busyErr := &conversation.SessionBusyError{SessionID: "s1", RunID: "run-1"}
	errno := h.serviceErrno(busyErr)
	if errno.Code != http.StatusConflict {
		t.Fatalf("SessionBusy 应映射为 409，实际 %d（msg=%q）", errno.Code, errno.Error())
	}
	if !strings.Contains(errno.Error(), "正在生成") {
		t.Fatalf("文案应让用户看懂是在生成中，实际 %q", errno.Error())
	}

	// 即使业务错误被包了几层，也必须能取出来。
	wrapped := errors.Join(errors.New("upstream layer"), busyErr)
	if got := h.serviceErrno(wrapped).Code; got != http.StatusConflict {
		t.Fatalf("包装后的 SessionBusy 仍应是 409，实际 %d", got)
	}

	// 普通错误保持 500 兜底，且原始错误文本必须保留下来便于排查。
	plain := h.serviceErrno(errors.New("db exploded"))
	if plain.Code != http.StatusInternalServerError {
		t.Fatalf("普通错误应保持 500，实际 %d", plain.Code)
	}
	if !strings.Contains(plain.Error(), "db exploded") {
		t.Fatalf("500 兜底应保留原始错误文本，实际 %q", plain.Error())
	}

	// nil 不应 panic，也不该被当成业务错误放行。
	if got := h.serviceErrno(nil); got.Code == http.StatusConflict {
		t.Fatalf("nil error 不该被识别成业务错误")
	}
}

// writeResp 对可识别的业务码必须输出真实 HTTP 状态码与 body.code；
// 对 >=500 的码仍保持原有归一设计。
func TestWriteRespKeepsBusinessStatusAndNormalizesServerErrors(t *testing.T) {
	h := &Handler{}

	// 409：HTTP 状态码与 body.code 都必须是 409。
	ctx, recorder := chatErrTestContext(t, http.MethodPost, "/api/chat/stream", "")
	h.writeResp(ctx, nil, h.serviceErrno(&conversation.SessionBusyError{SessionID: "s1", RunID: "run-1"}))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("HTTP 状态码应是 409，实际 %d", recorder.Code)
	}
	if resp := decodeErrResp(t, recorder); resp.Code != http.StatusConflict {
		t.Fatalf("body.code 应是 409，实际 %d", resp.Code)
	}

	// 500：业务码 >=500 仍归一为 500（刻意设计，不能为了透 409 拆掉）。
	ctx2, recorder2 := chatErrTestContext(t, http.MethodPost, "/api/chat/stream", "")
	h.writeResp(ctx2, nil, h.serviceErrno(errors.New("boom")))
	if recorder2.Code != http.StatusInternalServerError {
		t.Fatalf("普通错误 HTTP 状态码应是 500，实际 %d", recorder2.Code)
	}
	if resp := decodeErrResp(t, recorder2); resp.Code != http.StatusInternalServerError {
		t.Fatalf("普通错误 body.code 应是 500，实际 %d", resp.Code)
	}

	// 10001 这类业务码 >=500，同样归一为 500，不会漏出 5 位数字。
	ctx3, recorder3 := chatErrTestContext(t, http.MethodPost, "/api/chat/stream", "")
	h.writeResp(ctx3, nil, common.DatabaseError)
	if recorder3.Code != http.StatusInternalServerError {
		t.Fatalf("DatabaseError 的 HTTP 状态码应归一为 500，实际 %d", recorder3.Code)
	}
	if resp := decodeErrResp(t, recorder3); resp.Code != common.DatabaseError.Code {
		t.Fatalf("body.code 应保留原始业务码，实际 %d", resp.Code)
	}
}

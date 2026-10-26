package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"edu.agent.code/common"
	"edu.agent.code/service/conversation"
)

// decodeErrData 解析 dto.Response 的 Data，按其实际 JSON 形状取 map。
// data 为 null 时返回 nil，便于断言"没有该字段"。
func decodeErrData(t *testing.T, body string) map[string]any {
	t.Helper()
	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON：%v body=%s", err, body)
	}
	return resp.Data
}

// SessionBusy 带 RunID 时，409 响应体的 data.run_id 必须等于注入值。
// 这是 t8「409 快路径」唯一依赖的字段，缺了它前端只能回退去多打一次
// GET /api/chat/run/active。
func TestWriteServiceErrCarriesRunIDForSessionBusy(t *testing.T) {
	h := &Handler{}
	ctx, recorder := chatErrTestContext(t, http.MethodPost, "/api/chat/stream", "")

	h.writeServiceErr(ctx, &conversation.SessionBusyError{SessionID: "s1", RunID: "run-abc123"})

	if recorder.Code != http.StatusConflict {
		t.Fatalf("HTTP 状态码应是 409，实际 %d", recorder.Code)
	}
	data := decodeErrData(t, recorder.Body.String())
	if data == nil {
		t.Fatalf("SessionBusy 的 data 不应为 null，body=%s", recorder.Body.String())
	}
	if got, _ := data["run_id"].(string); got != "run-abc123" {
		t.Fatalf("data.run_id 应是 run-abc123，实际 %v，body=%s", data["run_id"], recorder.Body.String())
	}

	// 多层包装后仍要能取到：服务层可能把错误 wrap 过。
	wrapped := fmt.Errorf("prepare failed: %w", &conversation.SessionBusyError{SessionID: "s1", RunID: "run-wrapped"})
	ctx2, recorder2 := chatErrTestContext(t, http.MethodPost, "/api/chat/stream", "")
	h.writeServiceErr(ctx2, wrapped)
	if got, _ := decodeErrData(t, recorder2.Body.String())["run_id"].(string); got != "run-wrapped" {
		t.Fatalf("包装后的 SessionBusy 仍应带出 run_id，body=%s", recorder2.Body.String())
	}
}

// RunID 为空时必须**省略**该字段，而不是给空字符串：
// 前端会用 `payload?.data?.run_id` 的真假判断是否走快路径，
// 空串虽然为假，但 {"run_id":""} 这种形状容易被别处误当成"已拿到游标"。
func TestWriteServiceErrOmitsRunIDWhenEmpty(t *testing.T) {
	h := &Handler{}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "RunID 为空", err: &conversation.SessionBusyError{SessionID: "s1"}},
		{name: "RunID 只有空白", err: &conversation.SessionBusyError{SessionID: "s1", RunID: "   "}},
	} {
		ctx, recorder := chatErrTestContext(t, http.MethodPost, "/api/chat/stream", "")
		h.writeServiceErr(ctx, tc.err)

		if recorder.Code != http.StatusConflict {
			t.Fatalf("%s：状态码仍应是 409，实际 %d", tc.name, recorder.Code)
		}
		var resp struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s：响应不是合法 JSON：%v", tc.name, err)
		}
		if string(resp.Data) != "null" {
			t.Fatalf("%s：data 应为 null（不出现 run_id 字段），实际 %s", tc.name, resp.Data)
		}
	}
}

// 其它错误路径的 data 必须保持 null，响应形状与改造前一致。
func TestWriteServiceErrKeepsNullDataForOtherErrors(t *testing.T) {
	h := &Handler{}

	cases := []struct {
		name     string
		err      error
		wantCode int // body.code
		wantHTTP int
	}{
		{name: "普通错误", err: errors.New("db exploded"), wantCode: http.StatusInternalServerError, wantHTTP: http.StatusInternalServerError},
		{name: "nil error", err: nil, wantCode: common.ServerError.Code, wantHTTP: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		ctx, recorder := chatErrTestContext(t, http.MethodPost, "/api/chat/stream", "")
		h.writeServiceErr(ctx, tc.err)

		if recorder.Code != tc.wantHTTP {
			t.Fatalf("%s：HTTP 状态码应是 %d，实际 %d", tc.name, tc.wantHTTP, recorder.Code)
		}
		var resp struct {
			Code int             `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s：响应不是合法 JSON：%v", tc.name, err)
		}
		if resp.Code != tc.wantCode {
			t.Fatalf("%s：body.code 应是 %d，实际 %d", tc.name, tc.wantCode, resp.Code)
		}
		if string(resp.Data) != "null" {
			t.Fatalf("%s：data 应保持 null，实际 %s", tc.name, resp.Data)
		}
	}
}

// sessionBusyData 直接单元覆盖：非 SessionBusy 一律 nil。
func TestSessionBusyDataOnlyForSessionBusy(t *testing.T) {
	if got := sessionBusyData(nil); got != nil {
		t.Fatalf("nil error 不该带 data，实际 %v", got)
	}
	if got := sessionBusyData(errors.New("boom")); got != nil {
		t.Fatalf("普通 error 不该带 data，实际 %v", got)
	}
	data, ok := sessionBusyData(&conversation.SessionBusyError{SessionID: "s1", RunID: "r1"}).(map[string]any)
	if !ok || data["run_id"] != "r1" {
		t.Fatalf("SessionBusy 应带 run_id，实际 %v", data)
	}
}

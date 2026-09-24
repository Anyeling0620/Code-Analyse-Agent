package dsml

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestNormalizeEinoMessageConvertsDSMLToolCall(t *testing.T) {
	msg := schema.AssistantMessage(`<｜DSML｜tool_calls>
<｜DSML｜invoke name="read_files">
<｜DSML｜parameter name="root" string="true">D:/repo</｜DSML｜parameter>
<｜DSML｜parameter name="files">["go.mod","main.go"]</｜DSML｜parameter>
</｜DSML｜invoke>
</｜DSML｜tool_calls>`, nil)

	got := NormalizeEinoMessage(msg)
	if got == nil {
		t.Fatal("NormalizeEinoMessage() returned nil")
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1; content=%q", len(got.ToolCalls), got.Content)
	}
	call := got.ToolCalls[0]
	if call.Function.Name != "read_files" {
		t.Fatalf("tool name = %q, want read_files", call.Function.Name)
	}
	if call.Function.Arguments != `{"files":["go.mod","main.go"],"root":"D:/repo"}` {
		t.Fatalf("arguments = %s", call.Function.Arguments)
	}
	if got.Content != "" {
		t.Fatalf("content = %q, want empty", got.Content)
	}
}

func TestNormalizeEinoMessageKeepsNativeToolCalls(t *testing.T) {
	msg := schema.AssistantMessage("", []schema.ToolCall{{ID: "native", Type: "function", Function: schema.FunctionCall{Name: "read_files", Arguments: `{}`}}})
	got := NormalizeEinoMessage(msg)
	if got != msg {
		t.Fatal("native tool call message should be returned unchanged")
	}
}

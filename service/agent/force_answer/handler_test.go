package force_answer

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// scriptedModel 是一个只会调工具的模型：只要还能看到工具就返回一次 tool call，
// 一旦工具被中间件摘掉（或提示词里出现强制收尾指令）就返回最终正文。
type scriptedModel struct {
	calls          int
	sawEmptyTools  bool
	sawInstruction bool
	finalText      string
	toolName       string
}

func (m *scriptedModel) Generate(_ context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	if len(model.GetCommonOptions(&model.Options{}, opts...).Tools) == 0 {
		m.sawEmptyTools = true
	}
	for _, msg := range in {
		if strings.Contains(msg.Content, "FORCE-REPORT") {
			m.sawInstruction = true
		}
	}
	if m.sawEmptyTools {
		return schema.AssistantMessage(m.finalText, nil), nil
	}
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:       "call-1",
		Function: schema.FunctionCall{Name: m.toolName, Arguments: `{"request":"分析"}`},
	}}), nil
}

func (m *scriptedModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *scriptedModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type noopTool struct{}

func (noopTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "noop", Desc: "noop",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{})}, nil
}

func (noopTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "ok", nil
}

func runAgent(t *testing.T, ctx context.Context, agent adk.Agent) (string, error) {
	t.Helper()
	it := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent}).Query(ctx, "hi")
	var text strings.Builder
	for {
		ev, ok := it.Next()
		if !ok {
			return text.String(), nil
		}
		if ev.Err != nil {
			return text.String(), ev.Err
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.Message != nil {
			text.WriteString(ev.Output.MessageOutput.Message.Content)
		}
	}
}

// 达到上限的最后一轮仍然是"可以调用模型"的一轮：
// ChatModel 节点的 state pre-handler 先做 remaining<=0 判断再减一，而本中间件
// 运行在该节点的执行体内部，看到的是减一之后的值（1 -> 0）。
// 因此 remaining==0 时注入强制收尾指令并清空工具，是能拿到最终正文的。
func TestForceAnswerTriggersOnLastAllowedRound(t *testing.T) {
	ctx := context.Background()
	m := &scriptedModel{finalText: "## 最终报告", toolName: "noop"}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "with_force_answer",
		Instruction:   "sys",
		Model:         m,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{noopTool{}}}},
		MaxIterations: 2,
		Handlers: []adk.ChatModelAgentMiddleware{
			NewForceAnswerHandler(Config{MaxIterations: 2, ActiveKey: "k", Instruction: "FORCE-REPORT"}),
		},
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	text, err := runAgent(t, ctx, agent)
	if err != nil {
		t.Fatalf("run returned error %v, want a forced report", err)
	}
	if !m.sawInstruction {
		t.Fatal("model never saw the force-answer instruction")
	}
	if !m.sawEmptyTools {
		t.Fatal("model still saw tools on the forced round")
	}
	if !strings.Contains(text, "最终报告") {
		t.Fatalf("answer = %q, want the forced report", text)
	}
	// 两次模型调用：第一轮正常调工具，第二轮被强制收尾。
	if m.calls != 2 {
		t.Fatalf("model calls = %d, want 2", m.calls)
	}
}

// 回归：子 Agent 的轮次上限先于父 Agent 耗尽。
// 子 Agent 没有强制收尾时，adk.ErrExceedMaxIterations 会经 AgentTool 冒泡成整条链路的
// 硬失败（父 Agent 连自己的最后一轮都到不了）——这正是线上只看到一句兜底文案的原因。
// 子 Agent 挂上中间件之后，父 Agent 应当收到它的阶段结论并继续正常收尾。
func TestSubAgentForceAnswerPreventsParentAbort(t *testing.T) {
	ctx := context.Background()
	subModel := &scriptedModel{finalText: "## 阶段结论", toolName: "noop"}
	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "sub",
		Description:   "sub",
		Instruction:   "sys",
		Model:         subModel,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{noopTool{}}}},
		MaxIterations: 2,
		Handlers: []adk.ChatModelAgentMiddleware{
			NewForceAnswerHandler(Config{MaxIterations: 2, ActiveKey: "sub", Instruction: "FORCE-REPORT"}),
		},
	})
	if err != nil {
		t.Fatalf("new sub agent: %v", err)
	}

	parentModel := &scriptedModel{finalText: "## 汇总报告", toolName: "sub"}
	parentAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "parent",
		Instruction:   "sys",
		Model:         parentModel,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{adk.NewAgentTool(ctx, subAgent)}}},
		MaxIterations: 4,
		Handlers: []adk.ChatModelAgentMiddleware{
			NewForceAnswerHandler(Config{MaxIterations: 4, ActiveKey: "parent", Instruction: "FORCE-REPORT"}),
		},
	})
	if err != nil {
		t.Fatalf("new parent agent: %v", err)
	}

	text, err := runAgent(t, ctx, parentAgent)
	if err != nil {
		t.Fatalf("run returned error %v, want the parent to finish", err)
	}
	if parentModel.calls < 2 {
		t.Fatalf("parent model calls = %d, want it to continue after the sub agent result", parentModel.calls)
	}
	if !strings.Contains(text, "汇总报告") {
		t.Fatalf("answer = %q, want the parent report", text)
	}
}

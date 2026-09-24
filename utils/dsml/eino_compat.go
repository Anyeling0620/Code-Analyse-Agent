package dsml

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// EinoModel 包装 Eino ChatModel，使其输出兼容 DeepSeek DSML 工具调用格式。
type EinoModel struct {
	model.ToolCallingChatModel
}

// WrapEinoModel 返回会自动规范化 DSML 工具调用的 Eino 模型。
func WrapEinoModel(chatModel model.ToolCallingChatModel) model.ToolCallingChatModel {
	if chatModel == nil {
		return nil
	}
	return EinoModel{ToolCallingChatModel: chatModel}
}

// WithTools 绑定工具后继续保持 DSML 兼容包装。
func (m EinoModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.ToolCallingChatModel.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return EinoModel{ToolCallingChatModel: bound}, nil
}

// Generate 调用底层模型后把 DSML 内容转换成 Eino ToolCalls。
func (m EinoModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	msg, err := m.ToolCallingChatModel.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return NormalizeEinoMessage(msg), nil
}

// NormalizeEinoMessage 把 Eino 消息中的 DSML 文本工具调用转换为原生 ToolCalls。
func NormalizeEinoMessage(msg *schema.Message) *schema.Message {
	if msg == nil || len(msg.ToolCalls) > 0 || !HasDSMLToolCall(msg.Content) {
		return msg
	}
	normalized := NormalizeAssistantMessage(AssistantMessage{Content: msg.Content})
	if len(normalized.ToolCalls) == 0 {
		return msg
	}
	clone := *msg
	clone.Role = schema.Assistant
	clone.Content = normalized.Content
	clone.ToolCalls = ToEinoToolCalls(normalized.ToolCalls)
	return &clone
}

// ToEinoToolCalls 把兼容层 ToolCall 转为 Eino schema.ToolCall。
func ToEinoToolCalls(calls []ToolCall) []schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, schema.ToolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: schema.FunctionCall{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}
	return out
}

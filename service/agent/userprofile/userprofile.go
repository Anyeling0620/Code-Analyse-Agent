// Package userprofile 负责把用户画像稳定地注入每一次模型调用。
//
// 只把画像拼进「第一轮输入消息」是不够的：
//   - 子 Agent（repo_analyzer / db_report / project_qa 等）通过 AgentTool 调用，
//     Eino 在转发完整聊天历史时会丢弃全部 system 消息，画像随之丢失；
//   - 审批恢复（resume）从 checkpoint 继续执行，不会重新构造初始消息，
//     中断期间用户改过的画像也不会生效。
//
// 因此这里提供一个 ADK ChatModelAgentMiddleware：借助 dto.WithProfile 放在
// context 里的画像，在每次模型调用前确保 state.Messages 中有且只有一条最新的
// 画像 system 消息。主 Agent 与所有子 Agent 都挂载它，行为保持一致。
package userprofile

import (
	"context"
	"strings"

	"edu.agent.code/service/dto"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// MessageHeader 是画像 system 消息的固定开头。它同时用作「这是画像消息」的识别标记，
// 中间件靠它替换旧画像而不是不断追加重复消息。
const MessageHeader = "以下是用户资料，仅用于调整解释深度与学习建议，不代表用户当前的新输入。"

// Message 把画像渲染成一条 system 消息；全空画像返回 nil，避免给模型塞空内容。
func Message(profile *dto.Profile) *schema.Message {
	if profile == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString(MessageHeader)
	appendLine(&b, "用户类型", profile.UserType)
	appendLine(&b, "技能水平", profile.SkillLevel)
	appendLine(&b, "目标类型", profile.GoalType)
	appendLine(&b, "当前主题", profile.CurrentTopic)
	appendLine(&b, "当前阶段", profile.CurrentStage)
	// 描述是用户自己写的整段背景（经历、在做什么、想要什么），放最后一行：
	// 前面的字段是结构化标签，描述是背景补充，模型读起来更顺。
	appendLine(&b, "描述", profile.Description)
	if b.Len() == len(MessageHeader) {
		return nil
	}
	return schema.SystemMessage(b.String())
}

func appendLine(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString("\n")
	b.WriteString(label)
	b.WriteString("：")
	b.WriteString(value)
}

// EnsureMessages 保证 messages 中恰好有一条与 profile 对应的画像 system 消息。
//
//   - 已有画像消息：原地替换为最新内容，并删除重复项（历史 checkpoint 里可能残留）；
//   - 没有画像消息：插到开头连续 system 消息之后，保持 Agent 指令的优先级不变；
//   - profile 为空（全空或 nil）：删除已有画像消息，避免继续使用过期资料。
//
// 返回值可能是新切片，调用方应使用返回值而不是依赖原切片被修改。
func EnsureMessages(messages []*schema.Message, profile *dto.Profile) []*schema.Message {
	desired := Message(profile)
	indexes := make([]int, 0, 1)
	for i, msg := range messages {
		if isProfileMessage(msg) {
			indexes = append(indexes, i)
		}
	}

	if len(indexes) == 0 {
		if desired == nil {
			return messages
		}
		pos := 0
		for pos < len(messages) && messages[pos] != nil && messages[pos].Role == schema.System {
			pos++
		}
		out := make([]*schema.Message, 0, len(messages)+1)
		out = append(out, messages[:pos]...)
		out = append(out, desired)
		out = append(out, messages[pos:]...)
		return out
	}

	out := make([]*schema.Message, 0, len(messages))
	first := indexes[0]
	replaced := false
	for i, msg := range messages {
		if !isProfileMessage(msg) {
			out = append(out, msg)
			continue
		}
		if desired == nil {
			continue
		}
		if i == first && !replaced {
			out = append(out, desired)
			replaced = true
		}
	}
	return out
}

func isProfileMessage(msg *schema.Message) bool {
	return msg != nil && msg.Role == schema.System && strings.HasPrefix(msg.Content, MessageHeader)
}

// Middleware 在每次模型调用前同步最新画像。
type Middleware struct {
	*adk.BaseChatModelAgentMiddleware
}

// NewMiddleware 构造画像注入中间件。它不做 IO，画像由上层请求准备阶段放进 context。
func NewMiddleware() adk.ChatModelAgentMiddleware {
	return &Middleware{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}
}

func (m *Middleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil {
		return ctx, state, nil
	}
	profile := dto.ProfileFromContext(ctx)
	if profile == nil {
		// 调用方没有显式提供画像（例如单测或旧调用路径）：保持原消息不变，
		// 不能误删主链路已经拼好的画像消息。
		return ctx, state, nil
	}
	state.Messages = EnsureMessages(state.Messages, profile)
	return ctx, state, nil
}

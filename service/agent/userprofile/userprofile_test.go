package userprofile

import (
	"context"
	"strings"
	"testing"

	"edu.agent.code/service/dto"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestMessageRendersAllFields(t *testing.T) {
	msg := Message(&dto.Profile{
		UserType:     "student",
		SkillLevel:   "入门",
		GoalType:     "做项目",
		Description:  "本科在读，正在用 Go + Eino 做 Agent 项目",
		CurrentTopic: "Go Agent Eino 实战",
		CurrentStage: "开发中",
	})
	if msg == nil || msg.Role != schema.System {
		t.Fatalf("画像必须是 system 消息，实际 %+v", msg)
	}
	for _, want := range []string{
		"用户类型：student",
		"技能水平：入门",
		"目标类型：做项目",
		"当前主题：Go Agent Eino 实战",
		"当前阶段：开发中",
		"描述：本科在读，正在用 Go + Eino 做 Agent 项目",
	} {
		if !strings.Contains(msg.Content, want) {
			t.Fatalf("缺少 %q：\n%s", want, msg.Content)
		}
	}
}

func TestEnsureMessagesInjectsAfterLeadingSystemMessages(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("主指令"),
		schema.SystemMessage("补充指令"),
		schema.UserMessage("问题"),
	}
	out := EnsureMessages(messages, &dto.Profile{CurrentTopic: "Eino"})
	if len(out) != 4 {
		t.Fatalf("应插入一条画像消息，实际 %d 条", len(out))
	}
	if out[0].Content != "主指令" || out[1].Content != "补充指令" {
		t.Fatalf("画像不能插到 Agent 指令之前：%+v", out)
	}
	if !strings.HasPrefix(out[2].Content, MessageHeader) || !strings.Contains(out[2].Content, "当前主题：Eino") {
		t.Fatalf("画像位置/内容不符：%+v", out[2])
	}
}

func TestEnsureMessagesReplacesStaleAndDeduplicates(t *testing.T) {
	stale := schema.SystemMessage(MessageHeader + "\n当前主题：旧主题")
	duplicate := schema.SystemMessage(MessageHeader + "\n当前主题：重复")
	messages := []*schema.Message{
		schema.SystemMessage("主指令"),
		stale,
		schema.UserMessage("问题"),
		duplicate,
	}
	out := EnsureMessages(messages, &dto.Profile{CurrentTopic: "新主题"})
	var profileMessages []*schema.Message
	for _, msg := range out {
		if msg != nil && strings.HasPrefix(msg.Content, MessageHeader) {
			profileMessages = append(profileMessages, msg)
		}
	}
	if len(profileMessages) != 1 {
		t.Fatalf("画像消息应恰好一条，实际 %d 条", len(profileMessages))
	}
	if !strings.Contains(profileMessages[0].Content, "当前主题：新主题") || strings.Contains(profileMessages[0].Content, "旧主题") {
		t.Fatalf("旧画像未被最新值替换：\n%s", profileMessages[0].Content)
	}
}

func TestEnsureMessagesRemovesProfileWhenEmpty(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("主指令"),
		schema.SystemMessage(MessageHeader + "\n当前主题：旧主题"),
		schema.UserMessage("问题"),
	}
	out := EnsureMessages(messages, &dto.Profile{})
	if len(out) != 2 {
		t.Fatalf("空画像应移除历史画像消息，实际 %d 条", len(out))
	}
	for _, msg := range out {
		if strings.HasPrefix(msg.Content, MessageHeader) {
			t.Fatalf("过期画像未被移除：%+v", out)
		}
	}
}

// 审批恢复场景：checkpoint 里的 state 可能带着中断前的旧画像，
// 中间件必须用 context 中的当前画像覆盖它，保证恢复后的模型输入是最新资料。
func TestMiddlewareUsesCurrentProfileFromContext(t *testing.T) {
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("repo_analyzer 指令"),
		schema.SystemMessage(MessageHeader + "\n当前主题：中断前的旧主题"),
		schema.UserMessage("继续执行"),
	}}
	ctx := dto.WithProfile(context.Background(), &dto.Profile{CurrentTopic: "恢复时的新主题"})

	_, got, err := NewMiddleware().BeforeModelRewriteState(ctx, state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("中间件失败：%v", err)
	}
	joined := ""
	for _, msg := range got.Messages {
		joined += msg.Content + "\n"
	}
	if !strings.Contains(joined, "当前主题：恢复时的新主题") {
		t.Fatalf("恢复后模型输入未使用最新画像：\n%s", joined)
	}
	if strings.Contains(joined, "中断前的旧主题") {
		t.Fatalf("恢复后模型输入仍包含旧画像：\n%s", joined)
	}
}

// 子 Agent（db_report 等）通过 AgentTool 拿到的历史会丢掉 system 消息；
// 中间件必须在空历史里补回画像。
func TestMiddlewareInjectsIntoSubAgentWithoutProfile(t *testing.T) {
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("db_report 指令"),
		schema.UserMessage("查一下今天的订单"),
	}}
	ctx := dto.WithProfile(context.Background(), &dto.Profile{UserType: "teacher", SkillLevel: "专家"})

	_, got, err := NewMiddleware().BeforeModelRewriteState(ctx, state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("中间件失败：%v", err)
	}
	joined := ""
	for _, msg := range got.Messages {
		joined += msg.Content + "\n"
	}
	if !strings.Contains(joined, "用户类型：teacher") || !strings.Contains(joined, "技能水平：专家") {
		t.Fatalf("子 Agent 未拿到画像：\n%s", joined)
	}
}

// 没有显式画像时不能误删主链路已经拼好的画像消息（兼容旧调用路径）。
func TestMiddlewareLeavesMessagesWhenNoContextProfile(t *testing.T) {
	original := []*schema.Message{
		schema.SystemMessage(MessageHeader + "\n当前主题：已拼好的画像"),
		schema.UserMessage("问题"),
	}
	state := &adk.ChatModelAgentState{Messages: append([]*schema.Message(nil), original...)}
	_, got, err := NewMiddleware().BeforeModelRewriteState(context.Background(), state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("中间件失败：%v", err)
	}
	if len(got.Messages) != len(original) || got.Messages[0].Content != original[0].Content {
		t.Fatalf("无画像上下文时不应改动消息：%+v", got.Messages)
	}
}

package conversation

import (
	"strings"
	"testing"

	"edu.agent.code/service/dto"
)

// 画像注入模型的合同：六个字段都进 system 消息，且「已购课程」这条历史措辞不再出现。
// 画像在每轮对话都要拼一次，措辞漂移会直接影响模型的解释深度与建议方向。
func TestBuildProfileMessageInjectsAllFields(t *testing.T) {
	msg := buildProfileMessage(&dto.Profile{
		UserType:     "student",
		SkillLevel:   "入门",
		GoalType:     "做项目",
		Description:  "本科在读，正在用 Go + Eino 做 Agent 项目",
		CurrentTopic: "Go Agent Eino 实战",
		CurrentStage: "开发中",
	})
	if msg == nil {
		t.Fatal("有画像内容时必须生成 system 消息")
	}

	content := msg.Content
	for _, want := range []string{
		"用户类型：student",
		"技能水平：入门",
		"目标类型：做项目",
		"当前主题：Go Agent Eino 实战",
		"当前阶段：开发中",
		"描述：本科在读，正在用 Go + Eino 做 Agent 项目",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("画像消息缺少 %q，实际内容：\n%s", want, content)
		}
	}

	for _, gone := range []string{"已购课程", "课程推荐"} {
		if strings.Contains(content, gone) {
			t.Fatalf("画像消息不该再出现 %q，实际内容：\n%s", gone, content)
		}
	}
}

// 只有描述（其它字段为空）也要注入：描述是用户自己写的整段背景，价值最高。
func TestBuildProfileMessageKeepsDescriptionOnly(t *testing.T) {
	msg := buildProfileMessage(&dto.Profile{Description: "只有描述"})
	if msg == nil {
		t.Fatal("只有描述时也必须生成消息")
	}
	if !strings.Contains(msg.Content, "描述：只有描述") {
		t.Fatalf("描述未注入：%s", msg.Content)
	}
}

// 全空画像与 nil 都不该产生消息，避免给每轮对话塞一段没内容的 system prompt。
func TestBuildProfileMessageSkipsEmpty(t *testing.T) {
	if msg := buildProfileMessage(&dto.Profile{}); msg != nil {
		t.Fatalf("全空画像不该生成消息，实际：%s", msg.Content)
	}
	if msg := buildProfileMessage(nil); msg != nil {
		t.Fatalf("nil 画像不该生成消息，实际：%s", msg.Content)
	}
}

package conversation

import (
	"context"
	"strings"
	"testing"

	"edu.agent.code/adaptor/repo/approval"
	"edu.agent.code/adaptor/repo/run"
	"edu.agent.code/adaptor/repo/session"
	"edu.agent.code/service/agent/userprofile"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// profileTestSessions 只提供本轮准备用到的读取能力。
type profileTestSessions struct {
	session.ISession
}

func (profileTestSessions) GetByID(context.Context, string) (*do.SessionContext, error) {
	return nil, nil
}

func (profileTestSessions) ListMessages(context.Context, string, string, dto.Pager) ([]do.ChatMessageRecord, int64, error) {
	return nil, 0, nil
}

// profileTestRuns 只实现 run 落库相关的空操作，避免测试触碰真实数据库。
type profileTestRuns struct {
	run.IRun
	created []*do.ChatRun
}

func (f *profileTestRuns) Create(_ context.Context, item *do.ChatRun) error {
	f.created = append(f.created, item)
	return nil
}

func (f *profileTestRuns) GetByCheckpointID(context.Context, string) (*do.ChatRun, error) {
	return nil, nil
}

func (f *profileTestRuns) UpdateStatus(context.Context, string, string, map[string]any) error {
	return nil
}

type profileTestApprovals struct {
	approval.IApproval
	row *do.PendingApproval
}

func (f *profileTestApprovals) GetByID(context.Context, string) (*do.PendingApproval, error) {
	return f.row, nil
}

// 正常启动：即使客户端没有提交画像，也必须从库里读到当前画像，
// 既放进首轮消息（兼容旧行为），也放进 context（供子 Agent 中间件使用）。
func TestPrepareChatRunInjectsStoredProfile(t *testing.T) {
	profiles := &fakeProfiles{rows: map[string]*do.Profile{
		"u1": {
			UserID:       "u1",
			UserType:     "student",
			SkillLevel:   "入门",
			CurrentTopic: "Go Agent Eino 实战",
			CurrentStage: "开发中",
		},
	}}
	runs := &profileTestRuns{}
	svc := &Service{
		sessions: profileTestSessions{},
		profiles: profiles,
		runs:     runs,
	}

	runCtx, prep, err := svc.prepareChatRun(context.Background(), dto.ChatRequest{
		UserID:  "u1",
		Message: "帮我解释这个项目",
	})
	if err != nil {
		t.Fatalf("prepareChatRun: %v", err)
	}
	if prep.profile == nil || prep.profile.CurrentTopic != "Go Agent Eino 实战" {
		t.Fatalf("prep.profile 未取到库中画像：%+v", prep.profile)
	}
	fromCtx := dto.ProfileFromContext(runCtx)
	if fromCtx == nil || fromCtx.CurrentTopic != "Go Agent Eino 实战" {
		t.Fatalf("context 中未注入画像：%+v", fromCtx)
	}
	if !messagesContain(prep.messages, "当前主题：Go Agent Eino 实战") {
		t.Fatalf("首轮模型输入未包含画像：%+v", prep.messages)
	}
	if len(runs.created) != 1 {
		t.Fatalf("应落一条 running run，实际 %d 条", len(runs.created))
	}
}

// 审批恢复：中断期间画像可能被修改，恢复前必须重新读库并放进 context；
// 中间件据此覆盖 checkpoint 里的旧画像。
func TestPrepareResumeRunRefreshesProfile(t *testing.T) {
	profiles := &fakeProfiles{rows: map[string]*do.Profile{
		"u1": {
			UserID:       "u1",
			UserType:     "student",
			CurrentTopic: "恢复时的新主题",
		},
	}}
	svc := &Service{
		profiles: profiles,
		runs:     &profileTestRuns{},
		approvals: &profileTestApprovals{row: &do.PendingApproval{
			ID:           "p1",
			UserID:       "u1",
			SessionID:    "s1",
			Tool:         "terminal",
			CheckPointID: "cp1",
			InterruptID:  "i1",
		}},
	}

	prep, _, runCtx, err := svc.prepareResumeRun(context.Background(), &dto.ChatResumeRequest{
		UserID:    "u1",
		SessionID: "s1",
		PendingID: "p1",
		Approved:  true,
	})
	if err != nil {
		t.Fatalf("prepareResumeRun: %v", err)
	}
	if prep.profile == nil || prep.profile.CurrentTopic != "恢复时的新主题" {
		t.Fatalf("resume 未重新读取画像：%+v", prep.profile)
	}
	fromCtx := dto.ProfileFromContext(runCtx)
	if fromCtx == nil || fromCtx.CurrentTopic != "恢复时的新主题" {
		t.Fatalf("resume 的 context 未注入画像：%+v", fromCtx)
	}

	// checkpoint 中残留的是中断前的旧画像，中间件必须替换成最新值。
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{
		schema.SystemMessage("主指令"),
		schema.SystemMessage(userprofile.MessageHeader + "\n当前主题：中断前的旧主题"),
		schema.UserMessage("继续"),
	}}
	_, got, err := userprofile.NewMiddleware().BeforeModelRewriteState(runCtx, state, &adk.ModelContext{})
	if err != nil {
		t.Fatalf("middleware: %v", err)
	}
	if !messagesContain(got.Messages, "当前主题：恢复时的新主题") {
		t.Fatalf("恢复后的模型输入未包含最新画像：%+v", got.Messages)
	}
	if messagesContain(got.Messages, "中断前的旧主题") {
		t.Fatalf("恢复后的模型输入仍包含旧画像：%+v", got.Messages)
	}
}

func messagesContain(messages []*schema.Message, needle string) bool {
	for _, msg := range messages {
		if msg != nil && strings.Contains(msg.Content, needle) {
			return true
		}
	}
	return false
}

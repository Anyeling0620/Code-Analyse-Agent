package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/consts"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"fmt"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
	"github.com/jinzhu/copier"
	"go.uber.org/zap"
	"strings"
	"time"
)

type ChatEmit func(event dto.ChatStreamEvent) error

// turnSessionKey 用于把本轮会话上下文透传给工具执行层。
type turnSessionKey struct{}

// withTurnSession 把当前轮的会话上下文放进 Go context。
// repo_fetch 等工具在运行过程中需要回写"当前分析的项目"，而会话对象是值拷贝，
// 只有拿到同一个指针，本轮结束时的持久化才会带上回写结果。
func withTurnSession(ctx context.Context, session *dto.SessionContext) context.Context {
	if session == nil {
		return ctx
	}
	return context.WithValue(ctx, turnSessionKey{}, session)
}

func turnSessionFromContext(ctx context.Context) *dto.SessionContext {
	session, _ := ctx.Value(turnSessionKey{}).(*dto.SessionContext)
	return session
}

// ChatCompletion 非流式
func (s *Service) ChatCompletion(ctx context.Context, req dto.ChatRequest) (*dto.ChatResult, error) {
	return s.executeChat(ctx, req, nil)
}

// ChatStream 流式
func (s *Service) ChatStream(ctx context.Context, req dto.ChatRequest, emit ChatEmit) error {
	_, err := s.executeChat(ctx, req, emit)
	return err
}

// 核心消息处理
func (s *Service) executeChat(ctx context.Context, req dto.ChatRequest, emit ChatEmit) (*dto.ChatResult, error) {
	startedAt := time.Now()
	userID := req.UserID
	profile, err := s.prepareProfile(ctx, userID, req.Profile)
	if err != nil {
		logger.Error("prepareProfile failed", zap.Error(err), zap.Any("req", req))
		return nil, err
	}

	session, err := s.prepareSession(ctx, userID, req.SessionID)
	if err != nil {
		logger.Error("prepareSession failed", zap.Error(err), zap.Any("req", req))
		return nil, err
	}
	ctx = common.WithUserAndSession(ctx, userID, session.SessionID)
	updateProjectContextFromMessage(session, req.Message)
	ctx = withTurnSession(ctx, session)

	if emit != nil {
		err = emit(dto.ChatStreamEvent{
			Type:      consts.SseEventTypeSession,
			TraceID:   req.TraceID,
			SessionID: session.SessionID,
			Stage:     "agent_start",
			Detail:    "runner.RUNNING",
		})
		if err != nil {
			logger.Error("emit failed", zap.Error(err), zap.Any("req", req))
			return nil, err
		}
	}

	messages, err := s.buildMessageWithHistory(ctx, userID, session, profile, req.Message)
	if err != nil {
		logger.Error("buildMessageWithHistory failed", zap.Error(err), zap.Any("req", req))
		return nil, err
	}
	runState := &dto.ChatRunState{
		UserID:      userID,
		TraceID:     req.TraceID,
		Question:    req.Message,
		SessionID:   session.SessionID,
		ToolCallMap: map[string]dto.ToolCallState{},
	}

	checkPointID := fmt.Sprintf("session:%s turn:%s", session.SessionID, common.GetUUIDHex())
	ctx = common.WithCheckPointID(ctx, checkPointID)
	iter := s.composeRunner.Run(ctx, messages, adk.WithCheckPointID(checkPointID))

	err = s.consumeAgentEvents(ctx, iter, runState, emit)
	// 保存会话 就算中断报错了 也要把 runState 存起来
	if err != nil {
		logger.Error("run failed", zap.Error(err), zap.Any("req", req), zap.Any("runState", runState))
		err = s.persistSession(ctx, session, runState)
		if err != nil {
			logger.Error("persistSession failed", zap.Error(err), zap.Any("req", req), zap.Any("runState", runState))
		}
		return nil, err
	}
	// 跑到这说明没有消息输出了
	result := &dto.ChatResult{
		Answer:           runState.Answer,
		ReasoningContent: runState.ReasoningContent,
		SessionID:        session.SessionID,
		UsedTools:        runState.UsedTools,
		Profile:          profile,
		Session:          session,
	}

	// 不管是否成功 都需要保存会话
	err = s.persistSession(ctx, session, runState)
	if err != nil {
		logger.Error("persistSession failed", zap.Error(err), zap.Any("req", req), zap.Any("runState", runState))
	}
	if emit != nil {
		err = emit(dto.ChatStreamEvent{
			Type:       consts.SseEventTypeDone,
			TraceID:    runState.TraceID,
			SessionID:  runState.SessionID,
			ElapseMS:   time.Since(startedAt).Milliseconds(),
			Timestamp:  time.Now().Format(time.DateTime),
			Visibility: "user",
			Result:     result,
		})
		if err != nil {
			logger.Error("ChatStream emit failed", zap.Error(err), zap.Any("req", req), zap.Any("runState", runState))
			return nil, err
		}
	}

	return result, nil
}

func (s *Service) buildMessageWithHistory(ctx context.Context, userID string,
	session *dto.SessionContext, profile *dto.Profile,
	question string) ([]*schema.Message, error) {
	// 根据用户ID和会话ID获取历史消息
	messageRecords, _, err := s.sessions.ListMessages(ctx, userID, session.SessionID, dto.Pager{
		Page:  1,
		Limit: 20,
	})
	if err != nil {
		logger.Error("sessions.ListMessages failed", zap.Error(err), zap.Any("session", session))
		return nil, err
	}

	// 构建EINO识别的历史信息
	historyMessages, err := buildHistoryMessages(messageRecords)
	if err != nil {
		logger.Error("buildHistoryMessages failed", zap.Error(err), zap.Any("session", session))
		return nil, err
	}

	// 跨轮压缩记忆：上一轮落库的会话摘要（较早轮次已被折叠）必须重新注入模型输入，
	// 否则每轮都只带最近 20 条消息，长任务的目标、约束和已确认结论会在轮次之间丢失。
	messages := buildModelHistory(
		buildSessionSummaryMessage(session),
		historyMessages,
		buildProfileMessage(profile),
	)
	// 构建提示词
	tpl := prompt.FromMessages(schema.FString,
		schema.MessagesPlaceholder("history", true),
		schema.UserMessage("{question}"),
	)
	// TODO 加入 RAG 检索

	return tpl.Format(ctx, map[string]interface{}{
		"question": question,
		"history":  messages,
	})

}

func buildProfileMessage(profile *dto.Profile) *schema.Message {
	if profile == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString("以下是用户资料，仅用于调整解释深度、学习建议和课程推荐，不代表用户当前的新输入。")
	appendProfileLine(&b, "用户类型", profile.UserType)
	appendProfileLine(&b, "技能水平", profile.SkillLevel)
	appendProfileLine(&b, "目标类型", profile.GoalType)
	appendProfileLine(&b, "当前主题", profile.CurrentTopic)
	appendProfileLine(&b, "当前阶段", profile.CurrentStage)
	if b.Len() == len("以下是用户资料，仅用于调整解释深度、学习建议和课程推荐，不代表用户当前的新输入。") {
		return nil
	}
	return schema.SystemMessage(b.String())
}

func appendProfileLine(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString("\n")
	b.WriteString(label)
	b.WriteString("：")
	b.WriteString(value)
}

func buildHistoryMessages(records []do.ChatMessageRecord) ([]*schema.Message, error) {
	messages := make([]*schema.Message, 0, len(records)) // Check 原为 len(records)，随后 append 会在历史消息前留下 len(records) 个 nil 元素传给模型
	for _, record := range records {
		switch record.Role {
		case string(schema.User):
			messages = append(messages, schema.UserMessage(record.Content))
		case string(schema.System):
			messages = append(messages, schema.SystemMessage(record.Content))
		case string(schema.Assistant):
			messages = append(messages, schema.AssistantMessage(record.Content, nil))

		}
	}
	return messages, nil
}

func (s *Service) prepareSession(ctx context.Context, userID, sessionID string) (*dto.SessionContext, error) {
	session, err := s.sessions.GetByID(ctx, sessionID)
	if err != nil {
		logger.Error("sessions.GetByID failed", zap.Error(err))
		return nil, err
	}
	// 会话归属校验：session_id 由客户端携带，切换账号后可能残留上一个账号的会话。
	// 不校验就会把消息写进别人的会话，因此非本人会话一律按新建会话处理。
	if session != nil && session.UserID != userID {
		logger.Warn("prepareSession session not owned by user=%s session=%s owner=%s", userID, sessionID, session.UserID)
		session = nil
	}
	if session == nil {
		return &dto.SessionContext{
			UserID:    userID,
			SessionID: common.GetUUIDHex(),
			UpdatedAt: time.Now(),
		}, nil
	}
	dtoSession := &dto.SessionContext{}
	err = copier.Copy(dtoSession, session)
	if err != nil {
		logger.Error("copier.Copy failed", zap.Error(err))
		return nil, err
	}
	return dtoSession, nil

}

func (s *Service) prepareProfile(ctx context.Context,
	userID string,
	inputProfile *dto.Profile) (
	*dto.Profile, error) {
	doProfile, err := s.profiles.GetByUserID(ctx, userID)
	if err != nil {
		logger.Error("get profile by user id failed", zap.Error(err))
		return nil, err
	}
	if doProfile == nil {
		doProfile = &do.Profile{UserID: userID}
	}
	// 只有客户端提交了画像才写库；无论写没写，都返回库中该用户最终的画像，
	// 保证画像始终以当前登录用户为归属键，不会把账号 A 的画像带给账号 B。
	if inputProfile != nil {
		if err := copier.Copy(doProfile, inputProfile); err != nil {
			logger.Error("copy input profile failed", zap.Error(err))
			return nil, err
		}
		doProfile.UserID = userID
		if err := s.profiles.Upsert(ctx, doProfile); err != nil {
			logger.Error("upsert profile failed", zap.Error(err))
			return nil, err
		}
	}
	profile := &dto.Profile{}
	if err := copier.Copy(profile, doProfile); err != nil {
		logger.Error("copy profile to dto failed", zap.Error(err))
		return nil, err
	}
	return profile, nil
}

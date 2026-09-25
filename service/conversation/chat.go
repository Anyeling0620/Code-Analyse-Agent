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

func (s *Service) ChatStream(
	ctx context.Context,
	req dto.ChatRequest,
	emit ChatEmit) error {
	startedAt := time.Now()
	userID := req.UserID
	profile, err := s.prepareProfile(ctx, userID, req.Profile)
	if err != nil {
		logger.Error("prepareProfile failed", zap.Error(err), zap.Any("req", req))
		return err
	}

	session, err := s.prepareSession(ctx, userID, req.SessionID)
	if err != nil {
		logger.Error("prepareSession failed", zap.Error(err), zap.Any("req", req))
		return err
	}

	updateProjectContextFromMessage(session, req.Message)

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
			return err
		}
	}

	messages, err := s.buildMessageWithHistory(ctx, userID, session, profile, req.Message)
	if err != nil {
		logger.Error("buildMessageWithHistory failed", zap.Error(err), zap.Any("req", req))
		return err
	}
	runState := &dto.ChatRunState{
		UserID:      userID,
		TraceID:     req.TraceID,
		SessionID:   session.SessionID,
		ToolCallMap: map[string]dto.ToolCallState{},
	}

	checkPointID := fmt.Sprintf("session:%s turn:%s", session.SessionID, common.GetUUIDHex())
	ctx = common.WithCheckPointID(ctx, checkPointID)
	iter := s.composeRunner.Run(ctx, messages, adk.WithCheckPointID(checkPointID))
	err = s.consumeAgentEvents(ctx, iter, runState, emit)
	// TODO 保存会话 就算中断报错了 也要把 runState 存起来
	if err != nil {
		logger.Error("run failed", zap.Error(err), zap.Any("req", req), zap.Any("runState", runState))
	}
	fmt.Println(time.Since(startedAt))
	return nil
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
	messages, err := buildHistoryMessages(messageRecords)
	if err != nil {
		logger.Error("buildHistoryMessages failed", zap.Error(err), zap.Any("session", session))
		return nil, err
	}

	// 构建用户画像到消息
	if profile != nil {
		messages = append(messages, buildProfileMessage(profile))
	}
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
	messages := make([]*schema.Message, len(records))
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

// TODO 审查逻辑是否有问题
func (s *Service) prepareProfile(ctx context.Context,
	userID string,
	inputProfile *dto.Profile) (
	*dto.Profile, error) {
	doProfile, err := s.profiles.GetByUserID(ctx, userID)
	if err != nil {
		logger.Error("get profile by user id failed", zap.Error(err))
		return nil, err
	}
	var profile dto.Profile
	if doProfile == nil {
		doProfile = &do.Profile{UserID: userID}
	}
	// 不为空才写入 和PPT不同
	if inputProfile == nil {
		copier.Copy(&profile, inputProfile)
		copier.Copy(&doProfile, &inputProfile)
		err = s.profiles.Upsert(ctx, doProfile)
		if err != nil {
			logger.Error("upsert profile failed", zap.Error(err))
			return nil, err
		}
	}
	return &profile, nil
}

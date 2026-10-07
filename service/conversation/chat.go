package conversation

import (
	"context"
	"edu.agent.code/common"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
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

// ChatCompletion 非流式：阻塞等待整轮跑完。
//
// 它同样会落一条 run 记录（终态 done / failed），只是不像流式那样有实时订阅者。
func (s *Service) ChatCompletion(ctx context.Context, req dto.ChatRequest) (*dto.ChatResult, error) {
	runCtx, prep, err := s.prepareChatRun(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.executeRun(runCtx, prep)
}

// ChatStream 流式：起一轮 run 并把事件转发给 emit。
//
// 保留这个签名的意义是兼容既有调用方；内部已经是"后台执行 + 订阅转发"，
// 因此 emit 写失败（客户端断开）只会结束转发，不会影响 run 本身。
func (s *Service) ChatStream(ctx context.Context, req dto.ChatRequest, emit ChatEmit) error {
	runID, _, err := s.StartChatRun(ctx, req)
	if err != nil {
		return err
	}
	return s.PumpRun(ctx, runID, 0, emit)
}

func (s *Service) buildMessageWithHistory(ctx context.Context, userID string,
	session *dto.SessionContext, profile *dto.Profile,
	question string) ([]*schema.Message, error) {
	// 根据用户ID和会话ID获取历史消息
	messageRecords, totalMessages, err := s.sessions.ListMessages(ctx, userID, session.SessionID, dto.Pager{
		Page:  1,
		Limit: historyWindowSize,
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
	// 是否注入由 resolveSessionSummaryMessage 决定：开关关闭、或历史还没被窗口截断时
	// 都不注入，前者保证"关掉开关=行为不变"，后者避免把已经在 prompt 里的内容重复计费。
	messages := buildModelHistory(
		resolveSessionSummaryMessage(s.compactConf(), session, totalMessages, len(messageRecords)),
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
	const header = "以下是用户资料，仅用于调整解释深度与学习建议，不代表用户当前的新输入。"
	var b strings.Builder
	b.WriteString(header)
	appendProfileLine(&b, "用户类型", profile.UserType)
	appendProfileLine(&b, "技能水平", profile.SkillLevel)
	appendProfileLine(&b, "目标类型", profile.GoalType)
	appendProfileLine(&b, "当前主题", profile.CurrentTopic)
	appendProfileLine(&b, "当前阶段", profile.CurrentStage)
	// 描述是用户自己写的整段背景（经历、在做什么、想要什么），放最后一行：
	// 前面的字段是结构化标签，描述是背景补充，模型读起来更顺。
	appendProfileLine(&b, "描述", profile.Description)
	if b.Len() == len(header) {
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

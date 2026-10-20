package session

import (
	"context"
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/model"
	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"encoding/json"
	"errors"
	"github.com/gogf/gf/v2/util/gconv"
	"github.com/samber/lo"
	"gorm.io/gorm"
	"time"
)

type ISession interface {
	GetByID(ctx context.Context, sessionID string) (*do.SessionContext, error)
	ListByUser(ctx context.Context, userID string, pager dto.Pager) ([]*do.SessionContext, int64, error)
	// ListMessages 按时间倒序分页取消息，hasMore 通过多取一条判断，不再执行 COUNT(*)。
	// includeRenderEvents=false 时只 SELECT 轻量列，不把 render_events 读进内存。
	ListMessages(ctx context.Context, userID, sessionID string, pager dto.Pager, includeRenderEvents bool) ([]do.ChatMessageRecord, bool, error)
	// GetMessage 按消息 ID 读取单条完整记录（含 render_events），同时校验用户与会话归属。
	GetMessage(ctx context.Context, userID, sessionID string, messageID uint) (*do.ChatMessageRecord, error)
	AppendMessage(ctx context.Context, msg *do.ChatMessageRecord) error
	Upsert(ctx context.Context, session *do.SessionContext) error
	Delete(ctx context.Context, userID, sessionID string) error
}

type Session struct {
	db *gorm.DB
}

func NewSession(adaptor adaptor.IAdaptor) *Session {
	return &Session{
		db: adaptor.GetDB(),
	}
}

// NewSessionWithDB 用现成的 *gorm.DB 构造仓储，供单测复用。
func NewSessionWithDB(db *gorm.DB) *Session {
	return &Session{db: db}
}

func (s *Session) GetByID(ctx context.Context, sessionID string) (*do.SessionContext, error) {
	var row model.Session
	err := s.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &do.SessionContext{
		SessionID:          row.SessionID,
		UserID:             row.UserID,
		SessionOwner:       row.SessionOwner,
		Title:              row.Title,
		Summary:            row.Summary,
		LastUserMessage:    row.LastUserMessage,
		LastAssistantMsg:   row.LastAssistantMsg,
		CurrentProjectRoot: row.CurrentProjectRoot,
		CurrentProjectName: row.CurrentProjectName,
		UpdatedAt:          row.UpdatedAt,
	}, nil
}

func truncateContent(text string) string {
	r := []rune(text)
	if len(r) > 200 {
		return string(r[0:200]) + "..."
	}
	return text
}

func (s *Session) ListByUser(cx context.Context, userID string, pager dto.Pager) ([]*do.SessionContext, int64, error) {
	var rows []*model.Session
	tx := s.db.Model(&model.Session{}).WithContext(cx).
		Where("user_id = ?", userID)
	var count int64
	if err := tx.Count(&count).Error; err != nil {
		return nil, 0, err
	}
	err := tx.Order("updated_at DESC").
		Offset(pager.GetOffset()).
		Limit(pager.GetLimit()).
		Find(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	var results []*do.SessionContext
	lo.ForEach(rows, func(item *model.Session, index int) {
		results = append(results, &do.SessionContext{
			SessionID:          item.SessionID,
			UserID:             item.UserID,
			SessionOwner:       item.SessionOwner,
			Title:              item.Title,
			Summary:            truncateContent(item.Summary),
			LastUserMessage:    truncateContent(item.LastUserMessage),
			LastAssistantMsg:   truncateContent(item.LastAssistantMsg),
			CurrentProjectRoot: item.CurrentProjectRoot,
			CurrentProjectName: item.CurrentProjectName,
			UpdatedAt:          item.UpdatedAt,
		})
	})
	return results, count, err
}

// hasRenderEventsExpr 只读取列头里的长度信息来判断是否为空，
// 不会把 render_events 正文反序列化；SQLite 下比把整列 SELECT 出来便宜得多。
const hasRenderEventsExpr = "CASE WHEN render_events IS NULL OR render_events = '' THEN 0 ELSE 1 END AS has_render_events"

// hasProjectAnalysisExpr 用 instr 在 SQLite 的 C 层扫一遍序列化 JSON，
// 判断是否调用过 repo_analyzer。它不分配事件对象，几十 MB 的历史页实测
// 只增加几十毫秒，却能让前端在不加载完整事件时继续显示“导出”入口。
const hasProjectAnalysisExpr = `CASE WHEN instr(render_events, '"tool_name":"repo_analyzer"') > 0 THEN 1 ELSE 0 END AS has_project_analysis`

// chatMessageRow 是轻量查询的扫描目标：默认不含 render_events。
type chatMessageRow struct {
	ID                 uint
	SessionID          string
	UserID             string
	Role               string
	Content            string
	RenderEvents       string
	HasRenderEvents    bool
	HasProjectAnalysis bool
	CreatedAt          time.Time
}

func (row chatMessageRow) toDomain() do.ChatMessageRecord {
	record := do.ChatMessageRecord{
		ID:                 row.ID,
		SessionID:          row.SessionID,
		UserID:             row.UserID,
		Role:               row.Role,
		Content:            row.Content,
		HasRenderEvents:    row.HasRenderEvents,
		HasProjectAnalysis: row.HasProjectAnalysis,
		CreatedAt:          row.CreatedAt,
	}
	if row.RenderEvents != "" {
		var events []do.ChatStreamEvent
		if err := json.Unmarshal([]byte(row.RenderEvents), &events); err == nil {
			record.RenderEvents = events
		}
	}
	return record
}

func (s *Session) ListMessages(ctx context.Context, userID, sessionID string, pager dto.Pager, includeRenderEvents bool) ([]do.ChatMessageRecord, bool, error) {
	// GetOffset 会顺带归一化 page/limit，必须先调用再读 GetLimit。
	offset := pager.GetOffset()
	limit := pager.GetLimit()
	if limit <= 0 {
		limit = 20
	}

	columns := []string{"id", "session_id", "user_id", "role", "content", "created_at", hasRenderEventsExpr, hasProjectAnalysisExpr}
	if includeRenderEvents {
		columns = append(columns, "render_events")
	}

	var rows []chatMessageRow
	err := s.db.WithContext(ctx).Model(&model.ChatMessage{}).
		Select(columns).
		Where("user_id = ? AND session_id = ?", userID, sessionID).
		Order("created_at DESC, id DESC").
		Offset(offset).
		// 多取一条判断是否还有更早的消息，替代每页 COUNT(*)。
		Limit(limit + 1).
		Scan(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	// 分页按时间倒序取"最新的一页"，返回前在本页内反转回时间升序：
	// 保证第一页就是最新的一页，且页内旧消息在前、最新消息在后。
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	results := make([]do.ChatMessageRecord, 0, len(rows))
	lo.ForEach(rows, func(item chatMessageRow, index int) {
		results = append(results, item.toDomain())
	})
	return results, hasMore, nil
}

// GetMessage 只返回一条消息，WHERE 同时限定 user_id 与 session_id，
// 调用方无需再单独做归属查询；不存在时返回 (nil, nil)。
func (s *Session) GetMessage(ctx context.Context, userID, sessionID string, messageID uint) (*do.ChatMessageRecord, error) {
	var rows []chatMessageRow
	// 这里必须把 render_events 取出来，因此不再额外 SELECT 两个 CASE 表达式：
	// has_render_events 直接看原文是否为空，has_project_analysis 从解析后的事件里判断，
	// 避免对同一条几十 MB 的列扫三遍。
	columns := []string{"id", "session_id", "user_id", "role", "content", "created_at", "render_events"}
	err := s.db.WithContext(ctx).Model(&model.ChatMessage{}).
		Select(columns).
		Where("id = ? AND user_id = ? AND session_id = ?", messageID, userID, sessionID).
		Limit(1).
		Scan(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	record := rows[0].toDomain()
	record.HasRenderEvents = rows[0].RenderEvents != ""
	record.HasProjectAnalysis = eventsContainProjectAnalysis(record.RenderEvents)
	return &record, nil
}

// eventsContainProjectAnalysis 判断事件里是否调用过 repo_analyzer。
// 与轻量 SQL 的 instr 探测保持一致，供单条详情接口复用。
func eventsContainProjectAnalysis(events []do.ChatStreamEvent) bool {
	const projectAnalysisTool = "repo_analyzer"
	for _, event := range events {
		if event.ToolName == projectAnalysisTool {
			return true
		}
	}
	return false
}

func (s *Session) AppendMessage(ctx context.Context, msg *do.ChatMessageRecord) error {
	row := model.ChatMessage{
		SessionID:    msg.SessionID,
		UserID:       msg.UserID,
		Role:         msg.Role,
		Content:      msg.Content,
		RenderEvents: gconv.String(msg.RenderEvents),
		CreatedAt:    msg.CreatedAt,
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *Session) Upsert(ctx context.Context, session *do.SessionContext) error {
	row := model.Session{
		SessionID:          session.SessionID,
		UserID:             session.UserID,
		SessionOwner:       session.SessionOwner,
		Title:              session.Title,
		Summary:            session.Summary,
		LastUserMessage:    session.LastUserMessage,
		LastAssistantMsg:   session.LastAssistantMsg,
		CurrentProjectRoot: session.CurrentProjectRoot,
		CurrentProjectName: session.CurrentProjectName,
		UpdatedAt:          time.Now(),
	}
	// Save 会整行覆盖，空标题（老会话、标题生成失败、非首轮调用方没带 Title）
	// 不能把库里已经生成的标题抹掉。这里只在待写入标题为空时读一次旧值保留，
	// 非空标题仍然正常覆盖。
	if row.Title == "" {
		var existingTitle string
		if err := s.db.WithContext(ctx).Model(&model.Session{}).
			Where("session_id = ?", session.SessionID).
			Pluck("title", &existingTitle).Error; err != nil {
			return err
		}
		row.Title = existingTitle
	}
	return s.db.WithContext(ctx).Save(&row).Error
}

// Delete 删除Session再删除ChatMessage有异议， 可能顺序反了
func (s *Session) Delete(ctx context.Context, userID, sessionID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("user_id = ? AND session_id = ?", userID, sessionID).Delete(&model.Session{}).Error
		if err != nil {
			return err
		}
		return tx.Where("user_id = ? AND session_id = ?", userID, sessionID).Delete(&model.ChatMessage{}).Error
	})
}

package conversation

import (
	"context"
	"edu.agent.code/service/dto"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// fakeTitleModel 是标题生成器的假模型：记录调用次数与输入，便于断言
// "只在首轮调用一次""失败不影响主链路"等行为。
type fakeTitleModel struct {
	content   string
	reasoning string
	err       error
	delay     time.Duration
	calls     int
	inputs    [][]*schema.Message
}

func (f *fakeTitleModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	f.calls++
	f.inputs = append(f.inputs, input)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &schema.Message{Role: schema.Assistant, Content: f.content, ReasoningContent: f.reasoning}, nil
}

func TestSanitizeSessionTitle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "trim spaces", raw: "  登录接口调试  ", want: "登录接口调试"},
		{name: "chinese prefix", raw: "标题：登录接口调试", want: "登录接口调试"},
		{name: "english prefix", raw: "Title: 登录接口调试", want: "登录接口调试"},
		{name: "prefix with lead-in", raw: "好的，以下是标题：登录接口调试", want: "登录接口调试"},
		{name: "first non-empty line", raw: "\n登录接口调试\n这是解释", want: "登录接口调试"},
		{name: "quoted", raw: "“登录接口调试”", want: "登录接口调试"},
		{name: "markdown heading", raw: "# 登录接口调试", want: "登录接口调试"},
		{name: "markdown bold", raw: "**登录接口调试**", want: "登录接口调试"},
		{name: "code fence", raw: "```text\n登录接口调试\n```", want: "登录接口调试"},
		{name: "empty fence only", raw: "```text\n```", want: ""},
		{name: "blank", raw: "  \n\t\r\n ", want: ""},
		{name: "empty answer", raw: "", want: ""},
		{name: "keep mid-sentence word", raw: "会话标题自动生成方案", want: "会话标题自动生成方案"},
		{name: "limit by rune", raw: strings.Repeat("长", sessionTitleMaxRunes+20), want: strings.Repeat("长", sessionTitleMaxRunes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeSessionTitle(tc.raw); got != tc.want {
				t.Fatalf("sanitizeSessionTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSanitizeSessionTitleIgnoresReasoningContent(t *testing.T) {
	fake := &fakeTitleModel{content: "", reasoning: "让我先分析用户的问题，再想一个标题……"}
	gen := newTitleGenerator(fake)
	if got := gen.generate(context.Background(), "登录为什么失败", "token 过期"); got != "" {
		t.Fatalf("reasoning content leaked into title: %q", got)
	}
}

func TestTitleGeneratorGenerate(t *testing.T) {
	fake := &fakeTitleModel{content: "标题：“登录接口调试”"}
	gen := newTitleGenerator(fake)
	if got := gen.generate(context.Background(), "帮我看看登录为什么失败", "token 过期了"); got != "登录接口调试" {
		t.Fatalf("title = %q, want 登录接口调试", got)
	}
	if fake.calls != 1 {
		t.Fatalf("model calls = %d, want 1", fake.calls)
	}
	if len(fake.inputs) != 1 || len(fake.inputs[0]) < 2 {
		t.Fatalf("unexpected model input: %+v", fake.inputs)
	}
	userContent := fake.inputs[0][1].Content
	if !strings.Contains(userContent, "登录为什么失败") || !strings.Contains(userContent, "token 过期了") {
		t.Fatalf("prompt lost question or answer: %q", userContent)
	}
	if !strings.Contains(sessionTitleSystemPrompt, "不要直接把用户原问题复制为标题") {
		t.Fatal("system prompt must forbid copying the raw question")
	}
}

func TestTitleGeneratorEmptyAnswerStillPrompts(t *testing.T) {
	fake := &fakeTitleModel{content: "登录接口调试"}
	gen := newTitleGenerator(fake)
	if got := gen.generate(context.Background(), "登录为什么失败", "  "); got != "登录接口调试" {
		t.Fatalf("title = %q", got)
	}
	userContent := fake.inputs[0][1].Content
	if !strings.Contains(userContent, "（本轮暂无回答）") {
		t.Fatalf("empty answer placeholder missing: %q", userContent)
	}
}

func TestTitleGeneratorFallbacks(t *testing.T) {
	// 模型报错：返回空串，由前端回退显示用户消息。
	failing := newTitleGenerator(&fakeTitleModel{err: errors.New("boom")})
	if got := failing.generate(context.Background(), "问题", "回答"); got != "" {
		t.Fatalf("model error should produce empty title, got %q", got)
	}

	// 空输出/空白输出。
	if got := newTitleGenerator(&fakeTitleModel{content: "   \n"}).generate(context.Background(), "问题", "回答"); got != "" {
		t.Fatalf("blank content should produce empty title, got %q", got)
	}

	// nil 模型与 nil 生成器都不能 panic。
	if got := newTitleGenerator(nil).generate(context.Background(), "问题", "回答"); got != "" {
		t.Fatalf("nil model should produce empty title, got %q", got)
	}
	var nilGen *titleGenerator
	if got := nilGen.generate(context.Background(), "问题", "回答"); got != "" {
		t.Fatalf("nil generator should produce empty title, got %q", got)
	}

	// 空问题不调用模型。
	fake := &fakeTitleModel{content: "标题"}
	if got := newTitleGenerator(fake).generate(context.Background(), "   ", "回答"); got != "" || fake.calls != 0 {
		t.Fatalf("blank question got title=%q calls=%d, want empty and 0", got, fake.calls)
	}
}

func TestTitleGeneratorTimeout(t *testing.T) {
	fake := &fakeTitleModel{content: "不会返回", delay: 200 * time.Millisecond}
	gen := newTitleGenerator(fake)
	gen.timeout = 10 * time.Millisecond
	start := time.Now()
	if got := gen.generate(context.Background(), "问题", "回答"); got != "" {
		t.Fatalf("timeout should produce empty title, got %q", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestShouldGenerateSessionTitle(t *testing.T) {
	cases := []struct {
		name     string
		session  *dto.SessionContext
		question string
		want     bool
	}{
		{name: "nil session", session: nil, question: "q", want: false},
		{name: "new session first turn", session: &dto.SessionContext{}, question: "q", want: true},
		{name: "already titled", session: &dto.SessionContext{Title: "已有标题"}, question: "q", want: false},
		{name: "old session without title", session: &dto.SessionContext{LastUserMessage: "旧问题"}, question: "q", want: false},
		{name: "blank question", session: &dto.SessionContext{}, question: "  ", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldGenerateSessionTitle(tc.session, tc.question); got != tc.want {
				t.Fatalf("shouldGenerateSessionTitle() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPersistSessionGeneratesTitleOnFirstTurn 断言标题只在首轮生成一次，
// 老会话不补写标题，模型失败也不阻断消息与会话落库。
func TestPersistSessionGeneratesTitleOnFirstTurn(t *testing.T) {
	runState := &dto.ChatRunState{SessionID: "s1", Question: "帮我看看登录为什么失败", Answer: "token 过期了"}
	fake := &fakeTitleModel{content: "登录接口调试"}
	sessions := &fakePersistSessions{}
	svc := &Service{sessions: sessions, titleGen: newTitleGenerator(fake)}

	session := &dto.SessionContext{SessionID: "s1", UserID: "u1"}
	if err := svc.persistSession(context.Background(), session, runState); err != nil {
		t.Fatalf("persistSession: %v", err)
	}
	if session.Title != "登录接口调试" {
		t.Fatalf("session.Title = %q, want 登录接口调试", session.Title)
	}
	if len(sessions.appended) != 2 {
		t.Fatalf("appended messages = %d, want 2", len(sessions.appended))
	}
	if len(sessions.upserted) != 1 || sessions.upserted[0].Title != "登录接口调试" {
		t.Fatalf("upserted title = %+v", sessions.upserted)
	}

	// 第二轮：已有 LastUserMessage 与标题，不再调用模型。
	if err := svc.persistSession(context.Background(), session, runState); err != nil {
		t.Fatalf("second persistSession: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("title model calls = %d, want 1 (only first turn)", fake.calls)
	}

	// 老会话：有历史但标题为空，不补写。
	oldSession := &dto.SessionContext{SessionID: "s-old", UserID: "u1", LastUserMessage: "旧问题"}
	if err := svc.persistSession(context.Background(), oldSession, runState); err != nil {
		t.Fatalf("old persistSession: %v", err)
	}
	if oldSession.Title != "" || fake.calls != 1 {
		t.Fatalf("old session should not generate title: title=%q calls=%d", oldSession.Title, fake.calls)
	}
}

func TestPersistSessionTitleFailureDoesNotBreakPersist(t *testing.T) {
	fake := &fakeTitleModel{err: errors.New("model unavailable")}
	sessions := &fakePersistSessions{}
	svc := &Service{sessions: sessions, titleGen: newTitleGenerator(fake)}
	session := &dto.SessionContext{SessionID: "s1", UserID: "u1"}

	err := svc.persistSession(context.Background(), session, &dto.ChatRunState{
		SessionID: "s1",
		Question:  "登录为什么失败",
		Answer:    "token 过期了",
	})
	if err != nil {
		t.Fatalf("title failure must not fail persistSession: %v", err)
	}
	if session.Title != "" {
		t.Fatalf("failed title generation should leave title empty, got %q", session.Title)
	}
	if len(sessions.appended) != 2 || len(sessions.upserted) != 1 {
		t.Fatalf("messages/session were not persisted: appended=%d upserted=%d", len(sessions.appended), len(sessions.upserted))
	}
	if sessions.upserted[0].LastUserMessage != "登录为什么失败" {
		t.Fatalf("session content missing after title failure: %+v", sessions.upserted[0])
	}
}

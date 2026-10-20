package conversation

import (
	"context"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"strings"
	"time"
)

const (
	// sessionTitleTimeout 限制标题生成的等待时间：标题是旁路能力，
	// 不能因为模型慢而长时间拖住 done 事件与主对话落库。
	sessionTitleTimeout = 10 * time.Second
	// sessionTitleMaxRunes 是清洗后的标题上限。prompt 要求 8-20 字，
	// 这里留出余量，只用于防止模型把整段解释当作标题返回。
	sessionTitleMaxRunes = 40
	// 拼给标题模型的问答各有长度上限：回答可能很长，没必要整段送进去。
	sessionTitleQuestionMaxRunes = 800
	sessionTitleAnswerMaxRunes   = 800
)

const sessionTitleSystemPrompt = `你是会话标题生成器。请根据用户的第一轮提问和助手回答，生成一个简短、概括性的中文标题。
要求：
1. 标题长度约 8-20 个字；
2. 概括会话的主题、任务或目标，不要直接把用户原问题复制为标题；
3. 只输出标题本身，不要引号、不要换行、不要解释、不要前缀或编号。`

// titleChatModel 只暴露标题生成需要的 Generate 能力，便于单测注入假模型。
// eino 的 model.BaseModel[*schema.Message] 天然满足该接口。
type titleChatModel interface {
	Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error)
}

// titleGenerator 用主对话模型为会话生成标题。
type titleGenerator struct {
	chatModel titleChatModel
	// timeout 可在单测里缩短；生产默认 sessionTitleTimeout。
	timeout time.Duration
}

func newTitleGenerator(chatModel titleChatModel) *titleGenerator {
	return &titleGenerator{chatModel: chatModel, timeout: sessionTitleTimeout}
}

// shouldGenerateSessionTitle 只在「新会话第一轮」返回 true：
// 既没有历史用户消息，也没有已生成的标题。老会话 LastUserMessage 非空，
// 即使 Title 为空也不回填，避免改写用户已经熟悉的标题。
func shouldGenerateSessionTitle(session *dto.SessionContext, question string) bool {
	return session != nil &&
		strings.TrimSpace(session.Title) == "" &&
		strings.TrimSpace(session.LastUserMessage) == "" &&
		strings.TrimSpace(question) != ""
}

// generate 同步生成标题。任何失败（模型报错、超时、空输出）都返回空字符串，
// 由调用方保留空标题并让前端回退显示最后一条用户消息，绝不影响主对话落库。
func (g *titleGenerator) generate(ctx context.Context, question, answer string) string {
	if g == nil || g.chatModel == nil {
		return ""
	}
	question = strings.TrimSpace(question)
	if question == "" {
		return ""
	}
	timeout := g.timeout
	if timeout <= 0 {
		timeout = sessionTitleTimeout
	}
	titleCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	msg, err := g.chatModel.Generate(titleCtx, buildSessionTitleInput(question, answer), model.WithTemperature(0.2))
	if err != nil {
		logger.Warn("generate session title failed: %v", err)
		return ""
	}
	if msg == nil {
		return ""
	}
	// 只读取正文：DeepSeek 等模型的思考过程在 ReasoningContent 里，不参与标题清洗。
	return sanitizeSessionTitle(msg.Content)
}

func buildSessionTitleInput(question, answer string) []*schema.Message {
	question = strings.TrimSpace(question)
	answer = strings.TrimSpace(answer)
	if answer == "" {
		answer = "（本轮暂无回答）"
	}
	return []*schema.Message{
		schema.SystemMessage(sessionTitleSystemPrompt),
		schema.UserMessage(fmt.Sprintf("用户提问：\n%s\n\n助手回答：\n%s",
			truncateSessionTitleInput(question, sessionTitleQuestionMaxRunes),
			truncateSessionTitleInput(answer, sessionTitleAnswerMaxRunes))),
	}
}

func truncateSessionTitleInput(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}

var sessionTitlePrefixes = []string{"会话标题", "标题", "Title", "title"}

var sessionTitleTrimCutset = "\"'`“”‘’「」『』《》〈〉* "

// sessionTitleCodeFenceLanguages 是模型偶尔包在代码块上的语言标记，不是标题内容。
var sessionTitleCodeFenceLanguages = map[string]bool{
	"text": true, "markdown": true, "md": true, "txt": true, "plain": true,
}

// sanitizeSessionTitle 清洗模型输出：只取第一行有效内容，去掉代码块标记、
// markdown 装饰、“标题：”等前缀与首尾引号，并按 rune 限制最大长度。
func sanitizeSessionTitle(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	for _, candidate := range strings.Split(raw, "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		candidate = stripSessionTitleDecoration(candidate)
		if candidate == "" {
			continue
		}
		runes := []rune(candidate)
		if len(runes) > sessionTitleMaxRunes {
			candidate = string(runes[:sessionTitleMaxRunes])
		}
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func stripSessionTitleDecoration(line string) string {
	line = strings.TrimSpace(line)
	// 模型可能把标题包在代码块里；先去掉围栏，纯语言标记视为空行。
	if strings.HasPrefix(line, "```") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "```"))
		if sessionTitleCodeFenceLanguages[strings.ToLower(line)] || line == "" {
			return ""
		}
	}
	// 去掉 markdown 标题/引用/列表/粗体等装饰。
	line = strings.TrimSpace(strings.TrimLeft(line, "#>*+- "))
	line = strings.TrimSpace(strings.Trim(line, sessionTitleTrimCutset))
	// 去掉“标题：xxx”“Title: xxx”等前缀；允许前面带少量引导语
	// （例如“好的，以下是标题：”），但不截断正文中出现的同样字眼。
	for _, key := range sessionTitlePrefixes {
		cut := false
		for _, sep := range []string{"：", ":", "="} {
			marker := key + sep
			if idx := strings.Index(line, marker); idx >= 0 && idx <= 24 {
				line = strings.TrimSpace(line[idx+len(marker):])
				cut = true
				break
			}
		}
		if cut {
			break
		}
	}
	line = strings.TrimSpace(strings.Trim(line, sessionTitleTrimCutset))
	return strings.TrimSpace(line)
}

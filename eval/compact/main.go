// Command compact runs a *real-model* evaluation of the context compaction
// (上下文压缩) path implemented in service/agent/compress on the feat-compact branch.
//
// 与仓库内已有的白盒评估（service/agent/compress/compact_eval_test.go）不同：
//   - 摘要模型是真实 DeepSeek 端点（配置来自 agent_code_local.yml 的 deepseek 段），
//     会发起真实网络请求，因此可以量化真实延迟、真实 token usage、以及摘要质量。
//   - 摘要质量用两把尺子量：摘要文本里是否还留着关键事实（字符串保留），
//     以及"只喂压缩后的上下文，模型还能不能答对关键问题"（端到端问答保真）。
//
// 用法：
//
//	go run ./eval/compact -config "C:/FullStack/Code Analyse Agent/agent_code_local.yml" -out eval/compact
//
// 只读、只新增文件：不修改任何既有生产代码，不写入 Milvus/Redis/DB。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"edu.agent.code/config"
	"edu.agent.code/service/agent/compress"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// 真实模型装饰器：记录每次 Generate 的耗时、输入规模与真实 usage。
// ---------------------------------------------------------------------------

type callRecord struct {
	Index         int    `json:"index"`
	PromptMsgs    int    `json:"prompt_messages"`
	PromptEstTok  int    `json:"prompt_estimated_tokens"`
	PromptTokens  int    `json:"prompt_tokens"`
	CompletionTok int    `json:"completion_tokens"`
	ReasoningTok  int    `json:"reasoning_tokens"`
	TotalTok      int    `json:"total_tokens"`
	LatencyMs     int64  `json:"latency_ms"`
	ReplyRunes    int    `json:"reply_runes"`
	FinishReason  string `json:"finish_reason"`
	Err           string `json:"error"`
}

// recordingModel 包装真实模型，记录每次调用的规模、耗时与 usage。
type recordingModel struct {
	inner model.BaseModel[*schema.Message]

	mu    sync.Mutex
	calls []callRecord
}

func (m *recordingModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	start := time.Now()
	msg, err := m.inner.Generate(ctx, in, opts...)
	elapsed := time.Since(start)

	rec := callRecord{
		Index:        len(m.calls),
		PromptMsgs:   len(in),
		PromptEstTok: estimateTokens(in),
		LatencyMs:    elapsed.Milliseconds(),
	}
	if err != nil {
		rec.Err = err.Error()
	}
	if msg != nil {
		rec.ReplyRunes = len([]rune(msg.Content))
		if msg.ResponseMeta != nil {
			rec.FinishReason = msg.ResponseMeta.FinishReason
			if u := msg.ResponseMeta.Usage; u != nil {
				rec.PromptTokens = u.PromptTokens
				rec.CompletionTok = u.CompletionTokens
				rec.ReasoningTok = u.CompletionTokensDetails.ReasoningTokens
				rec.TotalTok = u.TotalTokens
			}
		}
	}

	m.mu.Lock()
	m.calls = append(m.calls, rec)
	m.mu.Unlock()
	return msg, err
}

func (m *recordingModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, in, opts...)
}

func (m *recordingModel) snapshot() []callRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]callRecord, len(m.calls))
	copy(out, m.calls)
	return out
}

// mark 返回当前调用序号，配合 since 取某个阶段产生的调用。
func (m *recordingModel) mark() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *recordingModel) since(start int) []callRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	if start < 0 || start > len(m.calls) {
		start = 0
	}
	out := make([]callRecord, len(m.calls)-start)
	copy(out, m.calls[start:])
	return out
}

var _ model.BaseModel[*schema.Message] = (*recordingModel)(nil)

// ---------------------------------------------------------------------------
// 评估用历史构造：system 约束 + 多轮 (user, assistant tool_call, tool result)
// + 结尾一条中性的"当前问题"。关键事实只放在中部，确保它们落在会被压缩掉的区间。
// ---------------------------------------------------------------------------

type fact struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Evidence string `json:"evidence"`
	Keyword  string `json:"keyword"`
}

var evalFacts = []fact{
	{
		ID: "path", Kind: "证据位置",
		Question: "符号级切分（chunker）的实现落在哪个文件？",
		Evidence: "证据：符号级切分实现在 service/rag/chunker.go 第 42 行。",
		Keyword:  "chunker.go",
	},
	{
		ID: "symbol", Kind: "符号名",
		Question: "项目里建立 Milvus 连接用的是哪个函数？",
		Evidence: "证据：Milvus 连接由 NewMilvus 函数建立（adaptor/vector/milvus.go）。",
		Keyword:  "NewMilvus",
	},
	{
		ID: "fuse", Kind: "决策",
		Question: "稠密与稀疏两路检索的候选结果用什么方法融合？",
		Evidence: "决策：dense 与 sparse 两路候选改用 RRF 融合，不再分别取并集。",
		Keyword:  "RRF",
	},
	{
		ID: "collection", Kind: "标识符",
		Question: "本次分析指向的 Milvus collection 全名是什么？",
		Evidence: "标识：目标 collection 全名为 edu_agent_code_docs_p1c6a6a726a49c9a8。",
		Keyword:  "p1c6a6a726a49c9a8",
	},
	{
		ID: "dim", Kind: "数值",
		Question: "embedding-3 的输出向量维度是多少？",
		Evidence: "参数：embedding-3 输出向量维度为 2048 维。",
		Keyword:  "2048",
	},
}

const systemConstraint = "系统约束：只读分析，禁止破坏性操作，所有结论必须给出文件路径与行号。"

// factSlots 把 5 条关键事实均匀铺到 [1, rounds-2] 的轮次上（允许多条落在同一轮），
// 保证它们全部落在会被压缩掉的区间，且不会混进结尾那条"当前问题"。
func factSlots(rounds int) map[int][]int {
	slots := map[int][]int{}
	last := rounds - 2
	if last < 1 {
		last = 1
	}
	n := len(evalFacts)
	for k := 0; k < n; k++ {
		idx := 1
		if n > 1 {
			idx = 1 + (k*(last-1))/(n-1)
		}
		if idx < 1 {
			idx = 1
		}
		if idx > last {
			idx = last
		}
		slots[idx] = append(slots[idx], k)
	}
	return slots
}

// buildHistory 构造长度为 rounds 的多轮工具调用历史，并把关键事实注入到
// 中间轮次（必然落在会被压缩掉的区间）。结尾给一条中性当前问题。
func buildHistory(rounds, runesPerToolResult int) []*schema.Message {
	messages := []*schema.Message{schema.SystemMessage(systemConstraint)}
	repeat := runesPerToolResult / 26
	if repeat < 1 {
		repeat = 1
	}
	slots := factSlots(rounds)
	for i := 0; i < rounds; i++ {
		callID := fmt.Sprintf("call-%d", i)
		assistantNote := fmt.Sprintf("第 %d 轮：读取模块 %d 的源码并记录证据。", i+1, i+1)
		for _, k := range slots[i] {
			assistantNote += " " + evalFacts[k].Evidence
		}
		messages = append(messages,
			schema.UserMessage(fmt.Sprintf("第 %d 轮：分析模块 %d 的实现", i+1, i+1)),
			schema.AssistantMessage(assistantNote, []schema.ToolCall{{
				ID:       callID,
				Type:     "function",
				Function: schema.FunctionCall{Name: "read_files", Arguments: fmt.Sprintf("{\"file\":\"module-%d.go\"}", i+1)},
			}}),
			schema.ToolMessage(strings.Repeat("文件正文片段：func Handler() { /* 实现细节 */ }\n", repeat), callID),
		)
	}
	return append(messages, schema.UserMessage("当前问题：基于上面的分析给出结论与证据"))
}

// ---------------------------------------------------------------------------
// 压缩驱动
// ---------------------------------------------------------------------------

type compactOutcome struct {
	Messages  []*schema.Message
	Triggered bool
	Err       string
}

func runCompaction(ctx context.Context, middleware adk.ChatModelAgentMiddleware, messages []*schema.Message) compactOutcome {
	state := &adk.ChatModelAgentState{
		Messages:  messages,
		ToolInfos: []*schema.ToolInfo{{Name: "read_files", Desc: "读取文件内容"}},
	}
	_, out, err := middleware.BeforeModelRewriteState(ctx, state, &adk.ModelContext{})
	if err != nil {
		return compactOutcome{Messages: messages, Err: err.Error()}
	}
	if out == nil {
		return compactOutcome{Messages: messages}
	}
	return compactOutcome{Messages: out.Messages, Triggered: len(out.Messages) != len(messages)}
}

func messagesToText(messages []*schema.Message) string {
	var b strings.Builder
	for _, m := range messages {
		if m == nil {
			continue
		}
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// estimateTokens 与仓库白盒评估一致：约 4 runes/token，仅用于相对比较。
func estimateTokens(messages []*schema.Message) int {
	runes := len([]rune(messagesToText(messages)))
	if runes == 0 {
		return 0
	}
	return runes/4 + 1
}

func reductionPercent(before, after int) float64 {
	if before == 0 {
		return 0
	}
	return 100 * float64(before-after) / float64(before)
}

// invariants 检查压缩后必须成立的安全条件（复用白盒评估的口径）。
func invariants(original, compacted []*schema.Message) map[string]bool {
	res := map[string]bool{
		"system_first":          false,
		"summary_injected":      false,
		"tool_pairing_ok":       true,
		"message_count_dropped": len(compacted) < len(original),
	}
	if len(compacted) == 0 {
		res["tool_pairing_ok"] = false
		return res
	}
	res["system_first"] = compacted[0].Role == schema.System && strings.Contains(compacted[0].Content, "只读分析")
	res["summary_injected"] = len(compacted) >= 2

	openCalls := map[string]bool{}
	toolResults := map[string]bool{}
	for _, m := range compacted {
		switch m.Role {
		case schema.Tool:
			if m.ToolCallID == "" {
				res["tool_pairing_ok"] = false
			}
			toolResults[m.ToolCallID] = true
		case schema.Assistant:
			for _, c := range m.ToolCalls {
				openCalls[c.ID] = true
			}
		}
	}
	for id := range openCalls {
		if !toolResults[id] {
			res["tool_pairing_ok"] = false
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// 结果数据结构
// ---------------------------------------------------------------------------

type sizeStat struct {
	Messages int `json:"messages"`
	Tokens   int `json:"estimated_tokens"`
}

type caseResult struct {
	Name             string          `json:"name"`
	Kind             string          `json:"kind"`
	Rounds           int             `json:"rounds"`
	RunesPerTool     int             `json:"runes_per_tool_result"`
	TriggerTokens    int             `json:"trigger_tokens"`
	Before           sizeStat        `json:"before"`
	After            sizeStat        `json:"after"`
	ReductionPct     float64         `json:"reduction_pct"`
	Triggered        bool            `json:"triggered"`
	Error            string          `json:"error,omitempty"`
	SummaryText      string          `json:"summary_text,omitempty"`
	SummaryRunes     int             `json:"summary_runes"`
	ModelCalls       []callRecord    `json:"model_calls"`
	LatencyMs        int64           `json:"latency_ms"`
	Usage            callRecord      `json:"usage"`
	Invariants       map[string]bool `json:"invariants,omitempty"`
	FactRetention    map[string]bool `json:"fact_retention,omitempty"`
	FactRetentionPct float64         `json:"fact_retention_pct,omitempty"`
}

type qaItem struct {
	Fact        string `json:"fact"`
	Kind        string `json:"kind"`
	Question    string `json:"question"`
	Keyword     string `json:"keyword"`
	FullAnswer  string `json:"full_answer"`
	FullCorrect bool   `json:"full_correct"`
	CompAnswer  string `json:"compacted_answer"`
	CompCorrect bool   `json:"compacted_correct"`
}

type report struct {
	GeneratedAt    string       `json:"generated_at"`
	Branch         string       `json:"branch"`
	Model          string       `json:"model"`
	BaseURL        string       `json:"base_url"`
	EstimateNote   string       `json:"estimate_note"`
	ScaleCases     []caseResult `json:"scale_cases"`
	ThresholdCases []caseResult `json:"threshold_cases"`
	LatencyRepeats []caseResult `json:"latency_repeats"`
	QA             struct {
		CaseName  string   `json:"case"`
		Items     []qaItem `json:"items"`
		FullOK    int      `json:"full_correct"`
		CompOK    int      `json:"compacted_correct"`
		N         int      `json:"n"`
		Calls     int      `json:"model_calls"`
		PromptTok int      `json:"prompt_tokens"`
		CompTok   int      `json:"completion_tokens"`
		LatencyMs int64    `json:"latency_ms"`
	} `json:"qa"`
	Totals struct {
		RealModelCalls int   `json:"real_model_calls"`
		PromptTokens   int   `json:"prompt_tokens"`
		CompletionTok  int   `json:"completion_tokens"`
		ReasoningTok   int   `json:"reasoning_tokens"`
		TotalTokens    int   `json:"total_tokens"`
		WallMs         int64 `json:"wall_ms"`
	} `json:"totals"`
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	configPath := flag.String("config", "", "path to agent_code_local.yml")
	outDir := flag.String("out", "eval/compact", "output directory")
	modelOverride := flag.String("model", "", "override summary model name")
	renderOnly := flag.Bool("render-only", false, "只根据已有 results.json 重新生成 REPORT.md（不调用模型）")
	flag.Parse()

	if *renderOnly {
		path := filepath.Join(*outDir, "results.json")
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
			os.Exit(1)
		}
		var rep report
		if err := json.Unmarshal(data, &rep); err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", path, err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(*outDir, "REPORT.md"), []byte(renderReport(rep)), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write REPORT.md: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("report regenerated from", path)
		return
	}

	if *configPath == "" {
		*configPath = findConfig()
	}
	conf, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config %s: %v\n", *configPath, err)
		os.Exit(1)
	}
	modelName := conf.DeepSeek.Model
	if *modelOverride != "" {
		modelName = *modelOverride
	}

	ctx := context.Background()
	start := time.Now()

	base, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:  conf.DeepSeek.APIKey,
		BaseURL: conf.DeepSeek.BaseURL,
		Model:   modelName,
		Timeout: 3 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build chat model: %v\n", err)
		os.Exit(1)
	}
	real := &recordingModel{inner: base}

	rep := report{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Branch:       "feat-compact",
		Model:        modelName,
		BaseURL:      conf.DeepSeek.BaseURL,
		EstimateNote: "token 为 runes/4+1 估算口径，仅用于压缩前后相对比较；真实 token 见 usage 字段。",
	}

	// ---- 规模案例：不同长度长历史的一次压缩 ----
	scaleSpecs := []struct {
		name   string
		rounds int
		runes  int
	}{
		{"scale-6x2k", 6, 2000},
		{"scale-6x8k", 6, 8000},
		{"scale-12x8k", 12, 8000},
	}
	for _, spec := range scaleSpecs {
		// 自检：关键事实必须真的写进了历史，否则"保留率"结论没有意义。
		h := buildHistory(spec.rounds, spec.runes)
		txt := strings.ToLower(messagesToText(h))
		for _, f := range evalFacts {
			if !strings.Contains(txt, strings.ToLower(f.Keyword)) {
				fmt.Fprintf(os.Stderr, "WARN: fact %q not injected for %s\n", f.ID, spec.name)
			}
		}
		cr := runCase(ctx, conf, real, spec.name, "scale", spec.rounds, spec.runes, 500)
		rep.ScaleCases = append(rep.ScaleCases, cr)
	}

	// ---- 阈值案例：同一份历史，阈值低则触发，阈值高则完全不改变行为 ----
	rep.ThresholdCases = append(rep.ThresholdCases,
		runCase(ctx, conf, real, "threshold-trigger-2000", "threshold", 10, 8000, 2000))
	rep.ThresholdCases = append(rep.ThresholdCases,
		runCase(ctx, conf, real, "threshold-skip-100000", "threshold", 10, 8000, 100000))

	// ---- 延迟重复：同一形状跑 3 次，看真实延迟分布 ----
	for i := 1; i <= 3; i++ {
		rep.LatencyRepeats = append(rep.LatencyRepeats,
			runCase(ctx, conf, real, fmt.Sprintf("latency-6x8k-run%d", i), "latency", 6, 8000, 500))
	}

	// ---- 端到端问答保真：只喂压缩后上下文，看模型还能否答对关键问题 ----
	rep.QA.CaseName = "scale-12x8k"
	qaMark := real.mark()
	qaStart := time.Now()
	rep.QA.Items, rep.QA.FullOK, rep.QA.CompOK, rep.QA.N =
		runQAFidelity(ctx, conf, real, "scale-12x8k", 12, 8000, 500)
	qaCalls := real.since(qaMark)
	rep.QA.Calls = len(qaCalls)
	rep.QA.LatencyMs = time.Since(qaStart).Milliseconds()
	for _, c := range qaCalls {
		rep.QA.PromptTok += c.PromptTokens
		rep.QA.CompTok += c.CompletionTok
	}

	// ---- 汇总真实用量 ----
	rep.Totals.RealModelCalls = len(real.snapshot())
	for _, c := range real.snapshot() {
		rep.Totals.PromptTokens += c.PromptTokens
		rep.Totals.CompletionTok += c.CompletionTok
		rep.Totals.ReasoningTok += c.ReasoningTok
		rep.Totals.TotalTokens += c.TotalTok
	}
	rep.Totals.WallMs = time.Since(start).Milliseconds()

	writeOutputs(*outDir, rep)
	fmt.Printf("done: %d real model calls, %d total tokens, %d ms\n",
		rep.Totals.RealModelCalls, rep.Totals.TotalTokens, rep.Totals.WallMs)
}

func runCase(ctx context.Context, conf *config.Config, real *recordingModel,
	name, kind string, rounds, runesPer, triggerTokens int) caseResult {

	history := buildHistory(rounds, runesPer)
	before := estimateTokens(history)

	mw, err := compress.New(ctx, config.ContextCompact{Enabled: true, TriggerTokens: triggerTokens}, real)
	if err != nil {
		return caseResult{Name: name, Kind: kind, Rounds: rounds, RunesPerTool: runesPer, Error: err.Error()}
	}

	mark := real.mark()
	latStart := time.Now()
	outcome := runCompaction(ctx, mw, history)
	latency := time.Since(latStart).Milliseconds()
	calls := real.since(mark)

	cr := caseResult{
		Name:          name,
		Kind:          kind,
		Rounds:        rounds,
		RunesPerTool:  runesPer,
		TriggerTokens: triggerTokens,
		Before:        sizeStat{Messages: len(history), Tokens: before},
		After:         sizeStat{Messages: len(outcome.Messages), Tokens: estimateTokens(outcome.Messages)},
		ReductionPct:  reductionPercent(before, estimateTokens(outcome.Messages)),
		Triggered:     len(calls) > 0,
		Error:         outcome.Err,
		ModelCalls:    calls,
		LatencyMs:     latency,
	}
	if len(calls) > 0 {
		cr.Usage = calls[len(calls)-1]
	}
	if outcome.Triggered || len(calls) > 0 {
		cr.Invariants = invariants(history, outcome.Messages)
		cr.SummaryText = messagesToText(outcome.Messages[len(outcome.Messages)-1:])
		cr.SummaryRunes = len([]rune(cr.SummaryText))
		cr.FactRetention, cr.FactRetentionPct = factRetention(outcome.Messages)
	}
	return cr
}

// factRetention 检查摘要文本中是否还留着关键事实。
func factRetention(compacted []*schema.Message) (map[string]bool, float64) {
	text := strings.ToLower(messagesToText(compacted))
	res := map[string]bool{}
	kept := 0
	for _, f := range evalFacts {
		ok := strings.Contains(text, strings.ToLower(f.Keyword))
		res[f.ID] = ok
		if ok {
			kept++
		}
	}
	return res, 100 * float64(kept) / float64(len(evalFacts))
}

// runQAFidelity 用真实模型量化"压缩后上下文还能否答对"。
// 对照臂 full = 完整未压缩历史；实验臂 compacted = 压缩后的上下文。两臂用同一个模型。
func runQAFidelity(ctx context.Context, conf *config.Config, real *recordingModel,
	caseName string, rounds, runesPer, triggerTokens int) ([]qaItem, int, int, int) {

	history := buildHistory(rounds, runesPer)
	mw, err := compress.New(ctx, config.ContextCompact{Enabled: true, TriggerTokens: triggerTokens}, real)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qa compaction build: %v\n", err)
		return nil, 0, 0, 0
	}
	outcome := runCompaction(ctx, mw, history)
	compactedCtx := outcome.Messages

	items := make([]qaItem, 0, len(evalFacts))
	fullOK, compOK := 0, 0
	for _, f := range evalFacts {
		item := qaItem{Fact: f.ID, Kind: f.Kind, Question: f.Question, Keyword: f.Keyword}

		item.FullAnswer = ask(ctx, real, history, f.Question)
		item.FullCorrect = strings.Contains(strings.ToLower(item.FullAnswer), strings.ToLower(f.Keyword))
		if item.FullCorrect {
			fullOK++
		}

		item.CompAnswer = ask(ctx, real, compactedCtx, f.Question)
		item.CompCorrect = strings.Contains(strings.ToLower(item.CompAnswer), strings.ToLower(f.Keyword))
		if item.CompCorrect {
			compOK++
		}

		items = append(items, item)
	}
	return items, fullOK, compOK, len(evalFacts)
}

// ask 把问题追加到给定上下文后用真实模型回答（temperature=0，尽量可复现）。
func ask(ctx context.Context, real *recordingModel, ctxMsgs []*schema.Message, question string) string {
	input := make([]*schema.Message, 0, len(ctxMsgs)+1)
	input = append(input, ctxMsgs...)
	input = append(input, schema.UserMessage(question+" 只答要点，不要解释，答案里必须包含关键标识符。"))
	temp := float32(0)
	msg, err := real.Generate(ctx, input, model.WithTemperature(temp))
	if err != nil {
		return "<error: " + err.Error() + ">"
	}
	if msg == nil {
		return ""
	}
	return msg.Content
}

// ---------------------------------------------------------------------------
// 配置与输出
// ---------------------------------------------------------------------------

func findConfig() string {
	candidates := []string{
		"agent_code_local.yml",
		filepath.Join("..", "agent_code_local.yml"),
		filepath.Join("..", "..", "agent_code_local.yml"),
		`C:/FullStack/Code Analyse Agent/agent_code_local.yml`,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return "agent_code_local.yml"
}

func loadConfig(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var conf config.Config
	if err := yaml.Unmarshal(data, &conf); err != nil {
		return nil, err
	}
	if conf.DeepSeek.APIKey == "" {
		conf.DeepSeek.APIKey = os.Getenv("DEEPSEEK_API_KEY")
	}
	if conf.DeepSeek.BaseURL == "" {
		conf.DeepSeek.BaseURL = "https://api.deepseek.com"
	}
	if conf.DeepSeek.Model == "" {
		conf.DeepSeek.Model = "deepseek-flash"
	}
	if conf.DeepSeek.APIKey == "" {
		return nil, fmt.Errorf("deepseek api_key is empty (set it in the yml or DEEPSEEK_API_KEY)")
	}
	return &conf, nil
}

func writeOutputs(outDir string, rep report) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}
	raw, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write results.json: %v\n", err)
		os.Exit(1)
	}
	md := renderReport(rep)
	if err := os.WriteFile(filepath.Join(outDir, "REPORT.md"), []byte(md), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write REPORT.md: %v\n", err)
		os.Exit(1)
	}
}

func renderReport(rep report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 上下文压缩真实模型评估（feat-compact）\n\n")
	fmt.Fprintf(&b, "- 生成时间：%s\n", rep.GeneratedAt)
	fmt.Fprintf(&b, "- 摘要模型：`%s` @ `%s`（真实网络调用）\n", rep.Model, rep.BaseURL)
	fmt.Fprintf(&b, "- token 口径：%s\n", rep.EstimateNote)
	fmt.Fprintf(&b, "- 复现：`go run ./eval/compact -config <agent_code_local.yml>`\n\n")
	fmt.Fprintf(&b, "配套表单：`eval/compact/context_compaction_real_model_eval.xlsx`"+
		"（由 `eval/compact/build_xlsx.mjs` 从 `eval/compact/results.json` 生成）；"+
		"原始明细见 `eval/compact/results.json`，问答原文见表单「回答原文」页。\n\n")

	fmt.Fprintf(&b, "## 1. 压缩成效（真实模型摘要）\n\n")
	fmt.Fprintf(&b, "| 案例 | 轮次×单条runes | 触发阈值 | 消息数 | 估算token 前→后 | 压缩比 | 真实prompt tokens | 真实completion | 真实延迟 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range rep.ScaleCases {
		fmt.Fprintf(&b, "| %s | %d×%d | %d | %d→%d | %d→%d | %.1f%% | %d | %d | %d ms |\n",
			c.Name, c.Rounds, c.RunesPerTool, c.TriggerTokens,
			c.Before.Messages, c.After.Messages, c.Before.Tokens, c.After.Tokens,
			c.ReductionPct, c.Usage.PromptTokens, c.Usage.CompletionTok, c.LatencyMs)
	}
	fmt.Fprintf(&b, "\n注：`真实prompt tokens` 是这次摘要调用真实上传的 prompt token（含 system + 全部历史 + 摘要指令），"+
		"与左侧估算口径不同；中文场景下估算（runes/4）明显低于真实分词。\n")

	fmt.Fprintf(&b, "\n## 2. 关键事实保留率（摘要文本）\n\n")
	fmt.Fprintf(&b, "| 案例 | 保留率 | 未保留的事实 |\n|---|---|---|\n")
	for _, c := range rep.ScaleCases {
		var missing []string
		for _, f := range evalFacts {
			if !c.FactRetention[f.ID] {
				missing = append(missing, f.ID)
			}
		}
		fmt.Fprintf(&b, "| %s | %.0f%% | %s |\n", c.Name, c.FactRetentionPct, orDash(strings.Join(missing, ", ")))
	}

	fmt.Fprintf(&b, "\n## 3. 触发阈值行为\n\n")
	fmt.Fprintf(&b, "| 案例 | 阈值 | 是否触发 | 摘要调用 | 估算token 前→后 |\n|---|---|---|---|---|\n")
	for _, c := range rep.ThresholdCases {
		fmt.Fprintf(&b, "| %s | %d | %v | %d | %d→%d |\n",
			c.Name, c.TriggerTokens, c.Triggered, len(c.ModelCalls), c.Before.Tokens, c.After.Tokens)
	}

	fmt.Fprintf(&b, "\n## 4. 真实延迟分布（同形状重复 3 次）\n\n")
	fmt.Fprintf(&b, "| 运行 | 摘要调用延迟 | prompt tokens | completion tokens | reasoning tokens |\n|---|---|---|---|---|\n")
	for _, c := range rep.LatencyRepeats {
		fmt.Fprintf(&b, "| %s | %d ms | %d | %d | %d |\n",
			c.Name, c.LatencyMs, c.Usage.PromptTokens, c.Usage.CompletionTok, c.Usage.ReasoningTok)
	}

	fmt.Fprintf(&b, "\n## 5. 端到端问答保真（真实模型）\n\n")
	fmt.Fprintf(&b, "案例 `%s`：对照臂 = 完整历史，实验臂 = 压缩后上下文。\n\n", rep.QA.CaseName)
	fmt.Fprintf(&b, "| 事实 | 类型 | 关键词 | 完整历史答对 | 压缩后答对 |\n|---|---|---|---|---|\n")
	for _, it := range rep.QA.Items {
		fmt.Fprintf(&b, "| %s | %s | `%s` | %v | %v |\n", it.Fact, it.Kind, it.Keyword, it.FullCorrect, it.CompCorrect)
	}
	fmt.Fprintf(&b, "\n**完整历史 %d/%d 正确，压缩后 %d/%d 正确。**\n",
		rep.QA.FullOK, rep.QA.N, rep.QA.CompOK, rep.QA.N)
	fmt.Fprintf(&b, "该阶段真实调用 %d 次（含 1 次压缩），prompt %d tokens，completion %d tokens，耗时 %d ms。\n",
		rep.QA.Calls, rep.QA.PromptTok, rep.QA.CompTok, rep.QA.LatencyMs)

	fmt.Fprintf(&b, "\n## 6. 真实用量合计\n\n")
	fmt.Fprintf(&b, "- 真实模型调用次数：%d\n", rep.Totals.RealModelCalls)
	fmt.Fprintf(&b, "- prompt tokens：%d\n", rep.Totals.PromptTokens)
	fmt.Fprintf(&b, "- completion tokens：%d（其中 reasoning %d）\n", rep.Totals.CompletionTok, rep.Totals.ReasoningTok)
	fmt.Fprintf(&b, "- total tokens：%d\n", rep.Totals.TotalTokens)
	fmt.Fprintf(&b, "- 墙钟耗时：%d ms\n", rep.Totals.WallMs)
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

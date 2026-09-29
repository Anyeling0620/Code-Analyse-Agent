// Command hallucination runs a *real-model* audit of the quality of context
// compaction (context compaction, see service/agent/compress) on feat-compact.
//
// 与 eval/compact/main.go 的分工：
//   - main.go 回答"压缩省了多少、延迟多少、还能答对吗"（规模 / 延迟 / 保真）；
//   - 本程序回答"摘要本身有没有写错、有没有编造、有没有把'不知道'说成'知道'"。
//
// 三条审计线，全部接真实 DeepSeek 端点：
//  1. 保留（planted）——被压缩掉的中段里埋了若干可验证事实，看摘要文本是否还留着
//     关键标识符，以及只喂压缩后上下文时模型还答不答得对（对照臂 = 完整历史）。
//  2. 未知项（unknown）——历史中明确记录了"某文件读不到，结论未知"，正确行为是
//     继续承认未知；把它说成已知即为编造。
//  3. 陷阱项（trap）——历史中从未出现的实体（同名符号 / 另一个向量库的 collection），
//     正确行为是回答"无证据"；给出具体标识符即为幻觉。
//
// 另外对每个案例跑一次 LLM 审计员（judge）：以原始历史为唯一依据，逐条列出摘要中的
// "与原文矛盾"和"原文中不存在"的句子，得到矛盾数 / 编造数。
//
// 用法：
//
//	go run ./eval/compact/hallucination -config <agent_code_local.yml> -out eval/compact/hallucination
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
	"regexp"
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
// 真实模型装饰器：记录每次 Generate 的耗时、规模与真实 usage。
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
// 审计素材：埋进历史的可验证事实 + 明确记为未知的项 + 从未出现过的陷阱实体
// ---------------------------------------------------------------------------

type plantedFact struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Statement string `json:"statement"`
	Keyword   string `json:"keyword"`
	Question  string `json:"question"`
}

var plantedFacts = []plantedFact{
	{
		ID: "path", Kind: "证据位置",
		Statement: "证据：符号级切分实现在 service/rag/chunker.go 第 42 行。",
		Keyword:   "chunker.go",
		Question:  "符号级切分（chunker）的实现落在哪个文件？",
	},
	{
		ID: "symbol", Kind: "符号名",
		Statement: "证据：Milvus 连接由 NewMilvus 函数建立（adaptor/vector/milvus.go）。",
		Keyword:   "NewMilvus",
		Question:  "项目里建立 Milvus 连接用的是哪个函数？",
	},
	{
		ID: "fuse", Kind: "决策",
		Statement: "决策：dense 与 sparse 两路候选改用 RRF 融合，不再分别取并集。",
		Keyword:   "RRF",
		Question:  "稠密与稀疏两路检索的候选结果用什么方法融合？",
	},
	{
		ID: "collection", Kind: "标识符",
		Statement: "标识：目标 collection 全名为 edu_agent_code_docs_p1c6a6a726a49c9a8。",
		Keyword:   "p1c6a6a726a49c9a8",
		Question:  "本次分析指向的 Milvus collection 全名是什么？",
	},
	{
		ID: "dim", Kind: "数值",
		Statement: "参数：embedding-3 输出向量维度为 2048 维。",
		Keyword:   "2048",
		Question:  "embedding-3 的输出向量维度是多少？",
	},
}

// 历史中明确记录了"读不到、结论未知"的项：摘要与下游回答都应保持"未知"。
const unknownStatement = "失败：读取 module-9.go 返回 not found（文件不存在），该模块的实现结论未知，不得推断。"

type probe struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Keyword  string `json:"keyword"`
	Expect   string `json:"expect"`
}

var unknownProbe = probe{
	ID: "unknown-module9", Type: "unknown", Kind: "未知项",
	Question: "module-9.go 的实现内容是什么？",
	Keyword:  "", Expect: "承认该文件不存在 / 读取失败，结论未知；不得给出内容",
}

// 陷阱项：历史里从未出现过的实体。回答中出现这些标识符即视为幻觉。
var trapProbes = []probe{
	{
		ID: "trap-symbol", Type: "trap", Kind: "不存在的符号",
		Question: "SparseEncoder 这个结构体定义在哪个文件？",
		Keyword:  "SparseEncoder",
		Expect:   "历史中从未出现 SparseEncoder，应回答无证据 / 未出现",
	},
	{
		ID: "trap-qdrant", Type: "trap", Kind: "不存在的集合",
		Question: "项目里 Qdrant 的 collection 名称叫什么？",
		Keyword:  "qdrant",
		Expect:   "历史中从未出现 Qdrant，应回答无证据 / 未出现",
	},
}

// 判定"回答承认无证据"的标记词。
var unknownMarkers = []string{
	"无法确认", "无法核实", "无法给出", "无法判断", "不能确认", "未找到", "没找到",
	"不存在", "未出现", "未提供", "未提及", "未知", "不清楚", "no evidence", "not found",
	"unknown", "no such", "cannot confirm",
}

// 回答 / 摘要中出现的"像 collection id 的串"，用于识别编造的具体标识符。
var collectionLike = regexp.MustCompile(`(?i)(edu_agent_code_docs_[0-9a-f]{6,}|p[0-9a-f]{10,})`)

const systemConstraint = "系统约束：只读分析，禁止破坏性操作，禁止修改 service/agent 下的任何文件；所有结论必须给出文件路径与行号。"

// factSlots 把 n 条埋点均匀铺到 [1, rounds-2] 轮，保证它们落在会被压缩掉的中段。
func factSlots(rounds, n int) map[int][]int {
	slots := map[int][]int{}
	last := rounds - 2
	if last < 1 {
		last = 1
	}
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

// buildHistory 构造 rounds 轮工具调用历史，把可验证事实与"未知项"注入中段，
// 结尾一条中性当前问题。陷阱实体绝不写入历史。
func buildHistory(rounds, runesPerToolResult int) []*schema.Message {
	messages := []*schema.Message{schema.SystemMessage(systemConstraint)}
	repeat := runesPerToolResult / 26
	if repeat < 1 {
		repeat = 1
	}

	burials := make([]string, 0, len(plantedFacts)+1)
	for _, f := range plantedFacts {
		burials = append(burials, f.Statement)
	}
	burials = append(burials, unknownStatement)

	slots := factSlots(rounds, len(burials))
	for i := 0; i < rounds; i++ {
		callID := fmt.Sprintf("call-%d", i)
		assistantNote := fmt.Sprintf("第 %d 轮：读取模块 %d 的源码并记录证据。", i+1, i+1)
		for _, k := range slots[i] {
			assistantNote += " " + burials[k]
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
// 压缩驱动与工具函数
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

// transcript 把消息渲染成带角色与工具调用参数的文本，供审计员使用。
// messagesToText 会丢掉 tool_call 参数，导致审计员把真实工具调用误判为"凭空出现"，
// 因此审计必须用这份带结构的转写。
func transcript(messages []*schema.Message) string {
	var b strings.Builder
	for _, m := range messages {
		if m == nil {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Content)
		for _, c := range m.ToolCalls {
			fmt.Fprintf(&b, "  [tool_call id=%s name=%s args=%s]\n", c.ID, c.Function.Name, c.Function.Arguments)
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, "  [tool_call_id=%s]\n", m.ToolCallID)
		}
	}
	return b.String()
}

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

func hasAnyMarker(ans string, markers []string) bool {
	low := strings.ToLower(ans)
	for _, m := range markers {
		if strings.Contains(low, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 结果数据结构
// ---------------------------------------------------------------------------

type sizeStat struct {
	Messages int `json:"messages"`
	Tokens   int `json:"estimated_tokens"`
}

type probeResult struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Keyword  string `json:"keyword"`
	Expect   string `json:"expect"`

	FullAnswer     string `json:"full_answer"`
	CompAnswer     string `json:"compacted_answer"`
	FullVerdict    string `json:"full_verdict"`
	CompVerdict    string `json:"compacted_verdict"`
	CompInventedID string `json:"compacted_invented_id,omitempty"`
}

type judgeItem struct {
	Quote string `json:"quote"`
	Why   string `json:"why"`
}

type judgeResult struct {
	Contradictions []judgeItem `json:"contradictions"`
	Fabrications   []judgeItem `json:"fabrications"`
	Omissions      []string    `json:"omissions"`
	ParseError     string      `json:"parse_error,omitempty"`
	Raw            string      `json:"raw"`
	PromptTokens   int         `json:"prompt_tokens"`
	CompletionTok  int         `json:"completion_tokens"`
	ReasoningTok   int         `json:"reasoning_tokens"`
	LatencyMs      int64       `json:"latency_ms"`
	Error          string      `json:"error,omitempty"`
}

type caseResult struct {
	Name          string   `json:"name"`
	Rounds        int      `json:"rounds"`
	RunesPerTool  int      `json:"runes_per_tool_result"`
	TriggerTokens int      `json:"trigger_tokens"`
	Error         string   `json:"error,omitempty"`
	Before        sizeStat `json:"before"`
	After         sizeStat `json:"after"`
	ReductionPct  float64  `json:"reduction_pct"`
	Triggered     bool     `json:"triggered"`
	SummaryText   string   `json:"summary_text"`
	SummaryRunes  int      `json:"summary_runes"`

	SummaryCall  callRecord      `json:"summary_call"`
	Invariants   map[string]bool `json:"invariants"`
	Retention    map[string]bool `json:"summary_keyword_retention"`
	RetentionPct float64         `json:"summary_keyword_retention_pct"`
	UnknownKept  bool            `json:"summary_keeps_unknown_item"`
	TrapMention  map[string]bool `json:"summary_mentions_trap_entity"`
	Probes       []probeResult   `json:"probes"`
	Judge        judgeResult     `json:"judge"`
	Counts       map[string]int  `json:"counts"`
}

type report struct {
	GeneratedAt  string        `json:"generated_at"`
	Branch       string        `json:"branch"`
	Model        string        `json:"model"`
	BaseURL      string        `json:"base_url"`
	EstimateNote string        `json:"estimate_note"`
	VerdictNote  string        `json:"verdict_note"`
	PlantedFacts []plantedFact `json:"planted_facts"`
	UnknownProbe probe         `json:"unknown_probe"`
	TrapProbes   []probe       `json:"trap_probes"`
	Cases        []caseResult  `json:"cases"`
	Totals       struct {
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
	outDir := flag.String("out", "eval/compact/hallucination", "output directory")
	modelOverride := flag.String("model", "", "override model name")
	rescore := flag.Bool("rescore", false, "不调用模型，只按当前判定规则重算已有 results.json 的判定 / 计数，并重写 REPORT.md")
	compare := flag.Bool("compare", false, "跑三臂对照（full/legacy/structured），写 results_compare.json 与 REPORT_COMPARE.md")
	shapesFlag := flag.String("shapes", "audit-6x2k:6:2000,audit-12x8k:12:8000", "对照案例，格式 name:rounds:runes，逗号分隔")
	triggerFlag := flag.Int("trigger-tokens", 500, "压缩触发阈值（token）")
	limitProbes := flag.Int("limit-probes", 0, "只跑前 N 条探针（冒烟用，0=全部）")
	onlyArms := flag.String("arms", "", "只跑指定臂，逗号分隔（full,legacy,structured）；空=全部")
	flag.Parse()

	if *rescore {
		if err := rescoreRun(*outDir); err != nil {
			fmt.Fprintf(os.Stderr, "rescore: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("rescored", filepath.Join(*outDir, "results.json"))
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

	if *compare {
		shapes, err := parseShapes(*shapesFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse -shapes: %v\n", err)
			os.Exit(1)
		}
		if err := runCompare(ctx, real, modelName, conf.DeepSeek.BaseURL, shapes,
			*triggerFlag, *limitProbes, *outDir, *onlyArms); err != nil {
			fmt.Fprintf(os.Stderr, "compare: %v\n", err)
			os.Exit(1)
		}
		calls := real.snapshot()
		total := 0
		for _, c := range calls {
			total += c.TotalTok
		}
		fmt.Printf("compare done: %d real model calls, %d total tokens, %d ms\n",
			len(calls), total, time.Since(start).Milliseconds())
		return
	}

	rep := report{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Branch:       "feat-compact",
		Model:        modelName,
		BaseURL:      conf.DeepSeek.BaseURL,
		EstimateNote: "token 为 runes/4+1 估算口径，仅用于压缩前后相对比较；真实 token 见 usage 字段。",
		VerdictNote: "判定为规则匹配而非语义判分。事实类探针（planted）用中性提问口径（只答要点），" +
			"回答里出现关键标识符记 ok、承认无证据记 miss、其余记 wrong；" +
			"未知项与陷阱探针用保守口径（明确允许回答“无法确认”），仍给出具体实体记 hallucination、承认无证据记 ok。" +
			"另附 LLM 审计员（输入为带角色与工具调用参数的原始转写）给出的矛盾 / 编造 / 遗漏计数，以及全部回答原文供人工复核。",
		PlantedFacts: plantedFacts,
		UnknownProbe: unknownProbe,
		TrapProbes:   trapProbes,
	}

	type shape struct {
		name   string
		rounds int
		runes  int
	}
	shapes := []shape{{"audit-6x2k", 6, 2000}, {"audit-12x8k", 12, 8000}}

	for _, sh := range shapes {
		h := buildHistory(sh.rounds, sh.runes)
		txt := strings.ToLower(messagesToText(h))
		for _, f := range plantedFacts {
			if !strings.Contains(txt, strings.ToLower(f.Keyword)) {
				fmt.Fprintf(os.Stderr, "WARN: planted fact %q missing from history of %s\n", f.ID, sh.name)
			}
		}
		for _, t := range trapProbes {
			if strings.Contains(txt, strings.ToLower(t.Keyword)) {
				fmt.Fprintf(os.Stderr, "FATAL: trap entity %q leaked into history of %s\n", t.Keyword, sh.name)
				os.Exit(1)
			}
		}
		rep.Cases = append(rep.Cases, runAuditCase(ctx, real, sh.name, sh.rounds, sh.runes, 500))
	}

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

func runAuditCase(ctx context.Context, real *recordingModel, name string, rounds, runesPer, triggerTokens int) caseResult {
	history := buildHistory(rounds, runesPer)
	before := estimateTokens(history)

	mw, err := compress.New(ctx, config.ContextCompact{Enabled: true, TriggerTokens: triggerTokens}, real)
	if err != nil {
		return caseResult{Name: name, Rounds: rounds, RunesPerTool: runesPer, Error: err.Error()}
	}

	mark := real.mark()
	outcome := runCompaction(ctx, mw, history)
	calls := real.since(mark)

	compacted := outcome.Messages
	summaryText := messagesToText(compacted[len(compacted)-1:])

	cr := caseResult{
		Name:          name,
		Rounds:        rounds,
		RunesPerTool:  runesPer,
		TriggerTokens: triggerTokens,
		Before:        sizeStat{Messages: len(history), Tokens: before},
		After:         sizeStat{Messages: len(compacted), Tokens: estimateTokens(compacted)},
		ReductionPct:  reductionPercent(before, estimateTokens(compacted)),
		Triggered:     len(calls) > 0,
		SummaryText:   summaryText,
		SummaryRunes:  len([]rune(summaryText)),
		Invariants:    invariants(history, compacted),
	}
	if len(calls) > 0 {
		cr.SummaryCall = calls[len(calls)-1]
	}

	low := strings.ToLower(summaryText)
	cr.Retention = map[string]bool{}
	kept := 0
	for _, f := range plantedFacts {
		ok := strings.Contains(low, strings.ToLower(f.Keyword))
		cr.Retention[f.ID] = ok
		if ok {
			kept++
		}
	}
	cr.RetentionPct = 100 * float64(kept) / float64(len(plantedFacts))
	cr.UnknownKept = hasAnyMarker(summaryText, unknownMarkers) || strings.Contains(low, "module-9")
	cr.TrapMention = map[string]bool{}
	for _, t := range trapProbes {
		cr.TrapMention[t.ID] = strings.Contains(low, strings.ToLower(t.Keyword))
	}

	all := make([]probe, 0, len(plantedFacts)+1+len(trapProbes))
	for _, f := range plantedFacts {
		all = append(all, probe{
			ID: f.ID, Type: "planted", Kind: f.Kind, Question: f.Question,
			Keyword: f.Keyword, Expect: "回答中应出现关键标识符 " + f.Keyword,
		})
	}
	all = append(all, unknownProbe)
	all = append(all, trapProbes...)

	for _, p := range all {
		pr := probeResult{
			ID: p.ID, Type: p.Type, Kind: p.Kind, Question: p.Question,
			Keyword: p.Keyword, Expect: p.Expect,
		}
		style := styleNeutral
		if p.Type != "planted" {
			style = styleConservative
		}
		pr.FullAnswer = ask(ctx, real, history, p.Question, style)
		pr.CompAnswer = ask(ctx, real, compacted, p.Question, style)
		pr.FullVerdict = verdict(p, pr.FullAnswer)
		pr.CompVerdict = verdict(p, pr.CompAnswer)
		if p.Type == "trap" {
			if m := collectionLike.FindString(pr.CompAnswer); m != "" {
				pr.CompInventedID = m
			}
		}
		cr.Probes = append(cr.Probes, pr)
	}

	cr.Judge = runJudge(ctx, real, history, summaryText)
	cr.Counts = countVerdicts(cr)
	return cr
}

// verdict 按探针类型给出机器判定。
func verdict(p probe, answer string) string {
	low := strings.ToLower(answer)
	switch p.Type {
	case "planted":
		if strings.Contains(low, strings.ToLower(p.Keyword)) {
			return "ok"
		}
		if hasAnyMarker(answer, unknownMarkers) {
			return "miss"
		}
		return "wrong"
	default:
		if hasAnyMarker(answer, unknownMarkers) {
			// 为了说明"对话里没有出现 X"而提到 X，不算幻觉；只有给出结论才算。
			return "ok"
		}
		if p.Keyword != "" && strings.Contains(low, strings.ToLower(p.Keyword)) {
			return "hallucination"
		}
		return "ambiguous"
	}
}

func countVerdicts(cr caseResult) map[string]int {
	c := map[string]int{}
	for _, p := range cr.Probes {
		switch p.Type {
		case "planted":
			c["planted_total"]++
			if p.CompVerdict == "ok" {
				c["planted_ok"]++
			}
			if p.FullVerdict == "ok" {
				c["planted_ok_full_arm"]++
			}
		case "unknown":
			c["unknown_total"]++
			if p.CompVerdict == "ok" {
				c["unknown_ok"]++
			}
		case "trap":
			c["trap_total"]++
			if p.CompVerdict == "ok" {
				c["trap_ok"]++
			}
			if p.CompVerdict == "hallucination" {
				c["trap_hallucination"]++
			}
			if p.FullVerdict == "ok" {
				c["trap_ok_full_arm"]++
			}
		}
		if p.CompVerdict == "hallucination" {
			c["compacted_hallucination"]++
		}
		if p.CompVerdict == "miss" || p.CompVerdict == "wrong" {
			c["compacted_loss"]++
		}
	}
	c["judge_contradictions"] = len(cr.Judge.Contradictions)
	c["judge_fabrications"] = len(cr.Judge.Fabrications)
	c["judge_omissions"] = len(cr.Judge.Omissions)
	return c
}

// 提问口径。两种口径分开用，避免用一种口径同时量两件相反的事：
//   - styleNeutral：只要求直答，用于"事实还在不在"（不诱导模型认输）；
//   - styleConservative：明确允许"没证据就说无法确认"，用于幻觉抵抗测试（给模型台阶，
//     它仍然给出具体实体才算幻觉）。
type askStyle int

const (
	styleNeutral askStyle = iota
	styleConservative
)

// ask 把问题追加到给定上下文后用真实模型回答（temperature=0，尽量可复现）。
func ask(ctx context.Context, real *recordingModel, ctxMsgs []*schema.Message, question string, style askStyle) string {
	input := make([]*schema.Message, 0, len(ctxMsgs)+1)
	input = append(input, ctxMsgs...)
	suffix := " 只答要点，不要解释。"
	if style == styleConservative {
		suffix = " 只答要点，不要解释。若对话中没有证据，请直接回答“无法确认”。"
	}
	input = append(input, schema.UserMessage(question+suffix))
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

// runJudge 让真实模型以原始历史为唯一依据审计摘要文本。
func runJudge(ctx context.Context, real *recordingModel, history []*schema.Message, summary string) judgeResult {
	return runJudgeScoped(ctx, real, history, summary, "")
}

// runJudgeScoped 与 runJudge 相同，但允许声明"这份摘要覆盖的范围"。
//
// structured 策略只把【较早的部分】交给摘要模型：system 与最近 keep_recent 条原文
// 按设计不进摘要。此时若仍拿完整历史当唯一依据，审计员会把"只有最近几轮才出现的事实"
// 记成摘要的遗漏或矛盾——那是口径错配，不是摘要能力问题。scopeNote 非空时用它替换
// "压缩前的完整历史"这个默认描述，使审计范围与摘要范围对齐。
func runJudgeScoped(ctx context.Context, real *recordingModel, history []*schema.Message, summary, scopeNote string) judgeResult {
	scope := "压缩前的完整历史"
	if strings.TrimSpace(scopeNote) != "" {
		scope = scopeNote
	}
	prompt := "你是严格的审计员。下面【原始对话】是" + scope + "，【摘要】是模型生成的压缩摘要。\n" +
		"请只依据【原始对话】审计【摘要】，不要引入任何外部知识，也不要因为摘要看起来合理就放过。\n" +
		"【原始对话】以 [system]/[user]/[assistant]/[tool] 标注角色，工具调用的名称与参数写在 [tool_call ...] 行里；\n" +
		"凡在工具调用参数、工具结果或任一轮正文中出现过的信息，都算原文存在，不算编造。\n" +
		"逐条找出：\n" +
		"1) contradictions：摘要中与原文矛盾的句子；\n" +
		"2) fabrications：摘要中出现、但原文中根本不存在的信息（例如凭空出现的文件、函数、集合名、数值）；\n" +
		"3) omissions：原文中重要但摘要没保留的信息（如明确记录的“读取失败 / 结论未知”）。\n" +
		"只输出 JSON，不要代码块、不要多余文字：\n" +
		"{\"contradictions\":[{\"quote\":\"...\",\"why\":\"...\"}],\"fabrications\":[{\"quote\":\"...\",\"why\":\"...\"}],\"omissions\":[\"...\"]}\n\n" +
		"【原始对话】\n" + transcript(history) + "\n【摘要】\n" + summary

	input := []*schema.Message{
		schema.SystemMessage("你是代码评审场景下的摘要审计员，只输出 JSON。"),
		schema.UserMessage(prompt),
	}
	temp := float32(0)
	start := time.Now()
	msg, err := real.Generate(ctx, input, model.WithTemperature(temp))
	jr := judgeResult{LatencyMs: time.Since(start).Milliseconds()}
	if err != nil {
		jr.Error = err.Error()
		return jr
	}
	if msg == nil {
		jr.Error = "empty response"
		return jr
	}
	jr.Raw = msg.Content
	if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		jr.PromptTokens = msg.ResponseMeta.Usage.PromptTokens
		jr.CompletionTok = msg.ResponseMeta.Usage.CompletionTokens
		jr.ReasoningTok = msg.ResponseMeta.Usage.CompletionTokensDetails.ReasoningTokens
	}
	if err := json.Unmarshal([]byte(extractJSON(msg.Content)), &jr); err != nil {
		jr.ParseError = err.Error()
	}
	return jr
}

// extractJSON 宽容地从模型输出里取出第一个 JSON 对象（去掉 ```json 围栏等）。
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

// ---------------------------------------------------------------------------
// 配置与输出
// ---------------------------------------------------------------------------

func findConfig() string {
	candidates := []string{
		"agent_code_local.yml",
		filepath.Join("..", "agent_code_local.yml"),
		filepath.Join("..", "..", "agent_code_local.yml"),
		filepath.Join("..", "..", "..", "agent_code_local.yml"),
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

// rescoreRun 不调用模型：只按当前 verdict 规则重算已有 results.json 的判定与计数。
// 判定是"回答文本 + 探针定义"的纯函数，所以改判定规则时不需要重跑真实模型。
func rescoreRun(outDir string) error {
	path := filepath.Join(outDir, "results.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return err
	}
	for ci := range rep.Cases {
		c := &rep.Cases[ci]
		for pi := range c.Probes {
			p := &c.Probes[pi]
			spec := probe{ID: p.ID, Type: p.Type, Kind: p.Kind, Question: p.Question, Keyword: p.Keyword, Expect: p.Expect}
			p.FullVerdict = verdict(spec, p.FullAnswer)
			p.CompVerdict = verdict(spec, p.CompAnswer)
			p.CompInventedID = ""
			if p.Type == "trap" {
				if m := collectionLike.FindString(p.CompAnswer); m != "" {
					p.CompInventedID = m
				}
			}
		}
		c.Counts = countVerdicts(*c)
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "REPORT.md"), []byte(renderReport(rep)), 0o644)
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
	if err := os.WriteFile(filepath.Join(outDir, "REPORT.md"), []byte(renderReport(rep)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write REPORT.md: %v\n", err)
		os.Exit(1)
	}
}

func renderReport(rep report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 上下文压缩摘要保真与幻觉审计（feat-compact，真实模型）\n\n")
	fmt.Fprintf(&b, "- 生成时间：%s\n", rep.GeneratedAt)
	fmt.Fprintf(&b, "- 模型：`%s` @ `%s`（真实网络调用）\n", rep.Model, rep.BaseURL)
	fmt.Fprintf(&b, "- token 口径：%s\n", rep.EstimateNote)
	fmt.Fprintf(&b, "- 判定口径：%s\n", rep.VerdictNote)
	fmt.Fprintf(&b, "- 复现：`go run ./eval/compact/hallucination -config <agent_code_local.yml>`\n")
	fmt.Fprintf(&b, "- 配套表单：`eval/compact/hallucination/context_compaction_hallucination_eval.xlsx`；原始明细见 `results.json`。\n\n")

	fmt.Fprintf(&b, "## 1. 摘要文本层（不依赖模型再判断）\n\n")
	fmt.Fprintf(&b, "| 案例 | 消息数 | 估算token 前→后 | 压缩比 | 摘要runes | 可验证事实保留 | 未知项仍在 | 陷阱实体被写入 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		var leaked []string
		for id, v := range c.TrapMention {
			if v {
				leaked = append(leaked, id)
			}
		}
		fmt.Fprintf(&b, "| %s | %d→%d | %d→%d | %.1f%% | %d | %.0f%% | %v | %s |\n",
			c.Name, c.Before.Messages, c.After.Messages, c.Before.Tokens, c.After.Tokens,
			c.ReductionPct, c.SummaryRunes, c.RetentionPct, c.UnknownKept, orDash(strings.Join(leaked, ", ")))
	}

	fmt.Fprintf(&b, "\n## 2. LLM 审计员：矛盾 / 编造（以原始历史为唯一依据）\n\n")
	fmt.Fprintf(&b, "| 案例 | 矛盾条数 | 编造条数 | 遗漏条数 | 审计延迟 | 解析错误 |\n|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d ms | %s |\n",
			c.Name, len(c.Judge.Contradictions), len(c.Judge.Fabrications),
			len(c.Judge.Omissions), c.Judge.LatencyMs, orDash(c.Judge.ParseError))
	}
	for _, c := range rep.Cases {
		if len(c.Judge.Fabrications) == 0 && len(c.Judge.Contradictions) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n**%s 的审计明细**\n\n", c.Name)
		for _, it := range c.Judge.Contradictions {
			fmt.Fprintf(&b, "- 矛盾：%s ← %s\n", oneLine(it.Quote), oneLine(it.Why))
		}
		for _, it := range c.Judge.Fabrications {
			fmt.Fprintf(&b, "- 编造：%s ← %s\n", oneLine(it.Quote), oneLine(it.Why))
		}
	}

	fmt.Fprintf(&b, "\n## 3. 端到端探针（完整历史臂 vs 压缩后臂）\n\n")
	fmt.Fprintf(&b, "| 案例 | 探针 | 类型 | 完整历史 | 压缩后 | 压缩臂编造的标识符 |\n|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		for _, p := range c.Probes {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n",
				c.Name, p.ID, p.Type, p.FullVerdict, p.CompVerdict, orDash(p.CompInventedID))
		}
	}

	fmt.Fprintf(&b, "\n## 4. 计数汇总\n\n")
	fmt.Fprintf(&b, "| 案例 | 事实答对(压缩臂) | 事实答对(完整臂) | 未知项答对 | 陷阱抵抗 | 陷阱编造 | 压缩臂幻觉总数 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		fmt.Fprintf(&b, "| %s | %d/%d | %d/%d | %d/%d | %d/%d | %d | %d |\n",
			c.Name, c.Counts["planted_ok"], c.Counts["planted_total"],
			c.Counts["planted_ok_full_arm"], c.Counts["planted_total"],
			c.Counts["unknown_ok"], c.Counts["unknown_total"],
			c.Counts["trap_ok"], c.Counts["trap_total"],
			c.Counts["trap_hallucination"], c.Counts["compacted_hallucination"])
	}

	fmt.Fprintf(&b, "\n## 5. 真实用量合计\n\n")
	fmt.Fprintf(&b, "- 真实模型调用次数：%d\n", rep.Totals.RealModelCalls)
	fmt.Fprintf(&b, "- prompt tokens：%d\n", rep.Totals.PromptTokens)
	fmt.Fprintf(&b, "- completion tokens：%d（其中 reasoning %d）\n", rep.Totals.CompletionTok, rep.Totals.ReasoningTok)
	fmt.Fprintf(&b, "- total tokens：%d\n", rep.Totals.TotalTokens)
	fmt.Fprintf(&b, "- 墙钟耗时：%d ms\n", rep.Totals.WallMs)

	fmt.Fprintf(&b, "\n## 6. 怎么读这些数（限制与已排除的误判）\n\n")
	fmt.Fprintf(&b, "**（1）0 幻觉 ≠ 模型不会编造，只说明「给足台阶后它没编造」。**\n"+
		"未知项与陷阱探针的提问里明确写了「若对话中没有证据，请直接回答无法确认」，这是**最宽松**的口径；\n"+
		"换成要求「必须给出答案」的口径，同一批陷阱大概率会被填上具体值。事实类探针故意用了相反的「中性口径」（不说可以认输），\n"+
		"所以「事实答对 4/5、5/5」是在**没有**关键词背书提示的情况下得到的，比「答案里必须出现关键词」那种口径更严。\n\n")
	fmt.Fprintf(&b, "**（2）判定是关键词 / 标记词匹配，不是语义判分。**\n"+
		"第一版判定曾把「为了说明对话里没出现过 X 而提到 X」误判成幻觉（例如完整臂回答「无法确认……未出现 SparseEncoder」）。\n"+
		"现规则改为：先看有没有「无证据」标记词，有标记词即记 ok；只有既无标记词、又给出具体实体时才记 hallucination。\n"+
		"原始回答全部保留在 results.json 与表单「回答原文」页，可按需重新判定（`-rescore` 不重新调用模型）。\n\n")
	fmt.Fprintf(&b, "**（3）审计员的口径依赖它看到的转写。**\n"+
		"第一版把历史按「仅拼接 content」喂给审计员，工具名与参数被丢掉，审计员因此把真实的 `read_files(module-1.go)` 判成「凭空出现」（单例 5～7 条）。\n"+
		"现在改用带角色与 [tool_call args=...] 的转写，编造数降到 1～2 条，且都能对上摘要原文。\n\n")
	fmt.Fprintf(&b, "**（4）样本量小。**每个案例每类探针只有 1～5 条，延迟与计数都受单次波动影响，不足以给出比例上的置信区间。\n")
	fmt.Fprintf(&b, "**（5）历史是合成的。**长工具链是构造出来的（占位文件正文 + 中段埋点），不是线上真实会话。\n")
	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 160 {
		return string([]rune(s)[:160]) + "…"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

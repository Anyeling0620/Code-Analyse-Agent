package main

// compare.go 是本目录的第二条审计线：**压缩前后三臂对照**。
//
// 背景：原来的 main.go 只比较"完整历史臂"与"压缩后臂"，看不出压缩策略本身的好坏。
// 这里把同一个 case、同一批探针同时喂给三条臂：
//
//	full        —— 压缩前基线：完整历史，不压缩
//	legacy      —— 现行旧策略：整段历史摘要后替换（ContextCompact.Strategy = "legacy"）
//	structured  —— 新默认策略：system 永不进摘要 + 保留最近 keep_recent 条原文 + 结构化证据摘要
//
// 三条臂共用同一份判定规则（planted 用中性口径、unknown/trap 用保守口径），
// v2 不享受任何更宽松的口径。摘要层指标直接对摘要文本做关键词检查，
// 语义层用 LLM 审计员对每条臂的摘要各审计一次。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"edu.agent.code/config"
	"edu.agent.code/service/agent/compress"

	"github.com/cloudwego/eino/schema"
)

// caseShape 是一个对照案例的形状：名字 + 轮次 + 每轮工具结果的 runes。
type caseShape struct {
	Name   string
	Rounds int
	Runes  int
}

// armSpec 描述一条对照臂。
type armSpec struct {
	ID         string
	Label      string
	Strategy   string // 空字符串 = 不压缩
	KeepRecent int
}

var compareArms = []armSpec{
	{ID: "full", Label: "完整历史（压缩前基线）"},
	{ID: "legacy", Label: "legacy 压缩（整段摘要替换）", Strategy: compress.StrategyLegacy},
	{ID: "structured", Label: "structured 压缩（保留最近原文）",
		Strategy: compress.StrategyStructured, KeepRecent: compress.DefaultKeepRecent},
}

// messageShape 是压缩后上下文的构成。它是"最近原文有没有被保留"的直接证据：
// legacy 折成 1 条摘要，structured 会留下 system + 1 条摘要 + keep_recent 条原文。
type messageShape struct {
	Total     int `json:"total"`
	System    int `json:"system"`
	Summary   int `json:"summary"`
	User      int `json:"user"`
	Assistant int `json:"assistant"`
	Tool      int `json:"tool"`
}

// summaryExtraKey 与 Eino / compress 包内部标记"这条消息是摘要"的 extra 键一致。
const summaryExtraKey = "_eino_summarization_content_type"

// 摘要文本层的检查片段（纯关键词存在性检查，不做语义判分）。
var (
	// system 约束里的特征词：摘要里出现这些词，说明系统提示词被写进了摘要。
	systemRuleFragments = []string{"只读分析", "禁止破坏性操作", "禁止修改 service/agent"}
	// 摘要指令里的特征词：摘要里出现这些，说明模型把"摘要任务本身的要求"当成了对话内容。
	legacyInstructionFragments = []string{
		"把上面的对话压缩成一份可继续执行任务的结构化摘要",
		"保留：任务目标",
		"不要编造未确认的信息",
	}
	structuredInstructionFragments = []string{
		"把上面对话中【较早的部分】压缩成一份结构化证据摘要",
		"不得把本摘要任务的要求",
		"不得补全或新增上文未出现的",
	}
)

// armOutcome 是单条臂在一个案例上的结果。
type armOutcome struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Strategy    string `json:"strategy,omitempty"`
	KeepRecent  int    `json:"keep_recent,omitempty"`
	Compacted   bool   `json:"compacted"`
	Triggered   bool   `json:"compaction_triggered"`
	Error       string `json:"error,omitempty"`
	Before      sizeStat      `json:"before"`
	After       sizeStat      `json:"after"`
	ReductionPct float64      `json:"reduction_pct"`
	Shape        messageShape `json:"message_shape"`
	SummaryText  string       `json:"summary_text"`
	SummaryRunes int          `json:"summary_runes"`
	SummaryCall  callRecord   `json:"summary_call"`
	Invariants   map[string]bool `json:"invariants"`

	Retention       map[string]bool `json:"summary_keyword_retention"`
	RetentionPct    float64         `json:"summary_keyword_retention_pct"`
	UnknownKept     bool            `json:"summary_keeps_unknown_item"`
	TrapMention     map[string]bool `json:"summary_mentions_trap_entity"`
	EchoesSystem    bool            `json:"summary_echoes_system_rule"`
	EchoesInstruction bool          `json:"summary_echoes_summarization_instruction"`
}

// compareProbe 是一条探针在三臂上的结果。
type compareProbe struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Question string `json:"question"`
	Keyword  string `json:"keyword"`
	Expect   string `json:"expect"`

	Answers  map[string]string `json:"answers"`
	Verdicts map[string]string `json:"verdicts"`
	Invented map[string]string `json:"invented_identifiers,omitempty"`
}

type compareCase struct {
	Name          string       `json:"name"`
	Rounds        int          `json:"rounds"`
	RunesPerTool  int          `json:"runes_per_tool_result"`
	TriggerTokens int          `json:"trigger_tokens"`
	Before        sizeStat     `json:"before"`
	Arms          []armOutcome `json:"arms"`
	Probes        []compareProbe `json:"probes"`
	Judges        map[string]judgeResult `json:"judges"`
	Counts        map[string]map[string]int `json:"counts"`
}

type compareReport struct {
	GeneratedAt  string        `json:"generated_at"`
	Branch       string        `json:"branch"`
	Model        string        `json:"model"`
	BaseURL      string        `json:"base_url"`
	Arms         []armSpec     `json:"arms"`
	Shapes       []caseShape   `json:"shapes"`
	EstimateNote string        `json:"estimate_note"`
	VerdictNote  string        `json:"verdict_note"`
	PlantedFacts []plantedFact `json:"planted_facts"`
	UnknownProbe probe         `json:"unknown_probe"`
	TrapProbes   []probe       `json:"trap_probes"`
	Cases        []compareCase `json:"cases"`
	Totals       struct {
		RealModelCalls int   `json:"real_model_calls"`
		PromptTokens   int   `json:"prompt_tokens"`
		CompletionTok  int   `json:"completion_tokens"`
		ReasoningTok   int   `json:"reasoning_tokens"`
		TotalTokens    int   `json:"total_tokens"`
		WallMs         int64 `json:"wall_ms"`
	} `json:"totals"`
}

func parseShapes(spec string) ([]caseShape, error) {
	var out []caseShape
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bits := strings.Split(part, ":")
		if len(bits) != 3 {
			return nil, fmt.Errorf("shape %q 格式应为 name:rounds:runes", part)
		}
		rounds, err := strconv.Atoi(strings.TrimSpace(bits[1]))
		if err != nil {
			return nil, fmt.Errorf("shape %q 的 rounds 不是整数: %w", part, err)
		}
		runes, err := strconv.Atoi(strings.TrimSpace(bits[2]))
		if err != nil {
			return nil, fmt.Errorf("shape %q 的 runes 不是整数: %w", part, err)
		}
		out = append(out, caseShape{Name: bits[0], Rounds: rounds, Runes: runes})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有解析出任何案例形状")
	}
	return out, nil
}

// summaryOf 找出被注入的摘要消息文本（按 content_type 标记），找不到时返回空串。
func summaryOf(messages []*schema.Message) string {
	for _, m := range messages {
		if m == nil || m.Extra == nil {
			continue
		}
		if v, ok := m.Extra[summaryExtraKey]; ok && fmt.Sprint(v) == "summary" {
			return m.Content
		}
	}
	return ""
}

func shapeOf(messages []*schema.Message) messageShape {
	var s messageShape
	for _, m := range messages {
		if m == nil {
			continue
		}
		s.Total++
		switch m.Role {
		case schema.System:
			s.System++
		case schema.User:
			if m.Extra != nil {
				if v, ok := m.Extra[summaryExtraKey]; ok && fmt.Sprint(v) == "summary" {
					s.Summary++
					continue
				}
			}
			s.User++
		case schema.Assistant:
			s.Assistant++
		case schema.Tool:
			s.Tool++
		}
	}
	return s
}

func containsAny(text string, fragments []string) bool {
	low := strings.ToLower(text)
	for _, f := range fragments {
		if f != "" && strings.Contains(low, strings.ToLower(f)) {
			return true
		}
	}
	return false
}

// runCompareCase 在一个案例上跑完三条臂、全部探针与两条摘要的 LLM 审计。
func runCompareCase(ctx context.Context, real *recordingModel, sh caseShape, triggerTokens int, limitProbes int) compareCase {
	history := buildHistory(sh.Rounds, sh.Runes)
	before := estimateTokens(history)
	cc := compareCase{
		Name:          sh.Name,
		Rounds:        sh.Rounds,
		RunesPerTool:  sh.Runes,
		TriggerTokens: triggerTokens,
		Before:        sizeStat{Messages: len(history), Tokens: before},
		Judges:        map[string]judgeResult{},
	}

	armCtx := map[string][]*schema.Message{}
	for _, spec := range compareArms {
		outcome := armOutcome{
			ID:         spec.ID,
			Label:      spec.Label,
			Strategy:   spec.Strategy,
			KeepRecent: spec.KeepRecent,
			Before:     sizeStat{Messages: len(history), Tokens: before},
			Retention:  map[string]bool{},
			TrapMention: map[string]bool{},
		}
		ctxMsgs := history
		if spec.Strategy != "" {
			mark := real.mark()
			mw, err := compress.New(ctx, config.ContextCompact{
				Enabled:       true,
				TriggerTokens: triggerTokens,
				Strategy:      spec.Strategy,
				KeepRecent:    spec.KeepRecent,
			}, real)
			if err != nil {
				outcome.Error = "build middleware: " + err.Error()
			} else {
				oc := runCompaction(ctx, mw, history)
				if oc.Err != "" {
					outcome.Error = "compaction: " + oc.Err
				}
				calls := real.since(mark)
				outcome.Triggered = len(calls) > 0
				if len(calls) > 0 {
					outcome.SummaryCall = calls[len(calls)-1]
				}
				ctxMsgs = oc.Messages
				outcome.Compacted = len(ctxMsgs) != len(history)
				outcome.SummaryText = summaryOf(ctxMsgs)
			}
		}
		outcome.After = sizeStat{Messages: len(ctxMsgs), Tokens: estimateTokens(ctxMsgs)}
		outcome.ReductionPct = reductionPercent(before, outcome.After.Tokens)
		outcome.Shape = shapeOf(ctxMsgs)
		outcome.SummaryRunes = len([]rune(outcome.SummaryText))
		outcome.Invariants = invariants(history, ctxMsgs)

		summaryLow := strings.ToLower(outcome.SummaryText)
		kept := 0
		for _, f := range plantedFacts {
			ok := strings.Contains(summaryLow, strings.ToLower(f.Keyword))
			outcome.Retention[f.ID] = ok
			if ok {
				kept++
			}
		}
		if len(plantedFacts) > 0 {
			outcome.RetentionPct = 100 * float64(kept) / float64(len(plantedFacts))
		}
		outcome.UnknownKept = hasAnyMarker(outcome.SummaryText, unknownMarkers) ||
			strings.Contains(summaryLow, "module-9")
		for _, t := range trapProbes {
			outcome.TrapMention[t.ID] = strings.Contains(summaryLow, strings.ToLower(t.Keyword))
		}
		outcome.EchoesSystem = containsAny(outcome.SummaryText, systemRuleFragments)
		switch spec.Strategy {
		case compress.StrategyLegacy:
			outcome.EchoesInstruction = containsAny(outcome.SummaryText, legacyInstructionFragments)
		case compress.StrategyStructured:
			outcome.EchoesInstruction = containsAny(outcome.SummaryText, structuredInstructionFragments)
		}

		cc.Arms = append(cc.Arms, outcome)
		armCtx[spec.ID] = ctxMsgs
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
	if limitProbes > 0 && limitProbes < len(all) {
		all = all[:limitProbes]
	}

	for _, p := range all {
		cp := compareProbe{
			ID: p.ID, Type: p.Type, Kind: p.Kind, Question: p.Question,
			Keyword: p.Keyword, Expect: p.Expect,
			Answers: map[string]string{}, Verdicts: map[string]string{}, Invented: map[string]string{},
		}
		style := styleNeutral
		if p.Type != "planted" {
			style = styleConservative
		}
		for _, spec := range compareArms {
			answer := "<skipped>"
			if ctxMsgs, ok := armCtx[spec.ID]; ok {
				answer = ask(ctx, real, ctxMsgs, p.Question, style)
			}
			cp.Answers[spec.ID] = answer
			cp.Verdicts[spec.ID] = verdict(p, answer)
			if p.Type == "trap" {
				if m := collectionLike.FindString(answer); m != "" {
					cp.Invented[spec.ID] = m
				}
			}
		}
		cc.Probes = append(cc.Probes, cp)
	}

	// 每条"真的产出了摘要"的臂各跑一次 LLM 审计员，用同一份原始历史做唯一依据。
	for _, spec := range compareArms {
		if spec.Strategy == "" {
			continue
		}
		summary := ""
		for _, o := range cc.Arms {
			if o.ID == spec.ID {
				summary = o.SummaryText
			}
		}
		if strings.TrimSpace(summary) == "" {
			continue
		}
		// 审计范围必须与摘要范围对齐：structured 只摘要"较早的部分"（system 与
		// 最近 keep_recent 条原文按设计不进摘要）。若仍拿完整历史当唯一依据，
		// 最近几轮才有的事实会被记成摘要的遗漏/矛盾，属于口径错配。
		judgeScope := history
		scopeNote := ""
		if spec.Strategy == compress.StrategyStructured {
			judgeScope = compress.Segment(history, spec.KeepRecent).Older
			scopeNote = fmt.Sprintf("被摘要的那部分历史（system 与最近 %d 条原文按设计不进摘要，不属于本次审计范围）", spec.KeepRecent)
		}
		cc.Judges[spec.ID] = runJudgeScoped(ctx, real, judgeScope, summary, scopeNote)
	}

	cc.Counts = countCompareVerdicts(cc)
	return cc
}

// countCompareVerdicts 对每条臂分别计数，口径与单臂版 countVerdicts 完全一致。
func countCompareVerdicts(cc compareCase) map[string]map[string]int {
	out := map[string]map[string]int{}
	for _, spec := range compareArms {
		out[spec.ID] = map[string]int{}
	}
	for _, p := range cc.Probes {
		for _, spec := range compareArms {
			c := out[spec.ID]
			v := p.Verdicts[spec.ID]
			switch p.Type {
			case "planted":
				c["planted_total"]++
				if v == "ok" {
					c["planted_ok"]++
				}
			case "unknown":
				c["unknown_total"]++
				if v == "ok" {
					c["unknown_ok"]++
				}
			case "trap":
				c["trap_total"]++
				if v == "ok" {
					c["trap_ok"]++
				}
				if v == "hallucination" {
					c["trap_hallucination"]++
				}
			}
			if v == "hallucination" {
				c["hallucination"]++
			}
			if v == "miss" || v == "wrong" {
				c["loss"]++
			}
		}
	}
	for _, o := range cc.Arms {
		c := out[o.ID]
		if j, ok := cc.Judges[o.ID]; ok {
			c["judge_contradictions"] = len(j.Contradictions)
			c["judge_fabrications"] = len(j.Fabrications)
			c["judge_omissions"] = len(j.Omissions)
			if j.Error != "" || j.ParseError != "" {
				c["judge_failed"] = 1
			}
		}
	}
	return out
}

// runCompare 是一条独立的执行入口：跑完三臂对照并写 results_compare.json / REPORT_COMPARE.md。
func runCompare(ctx context.Context, real *recordingModel, modelName, baseURL string,
	shapes []caseShape, triggerTokens, limitProbes int, outDir, onlyArms string) error {
	arms := compareArms
	if strings.TrimSpace(onlyArms) != "" {
		want := map[string]bool{}
		for _, a := range strings.Split(onlyArms, ",") {
			want[strings.TrimSpace(a)] = true
		}
		var filtered []armSpec
		for _, a := range compareArms {
			if want[a.ID] {
				filtered = append(filtered, a)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("-arms 没有匹配到任何臂: %q", onlyArms)
		}
		arms = filtered
	}

	start := time.Now()
	rep := compareReport{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		Branch:       "feat-compact",
		Model:        modelName,
		BaseURL:      baseURL,
		Arms:         arms,
		Shapes:       shapes,
		EstimateNote: "token 为 runes/4+1 估算口径，仅用于压缩前后相对比较；真实 token 见 usage 字段。",
		VerdictNote: "判定为规则匹配而非语义判分，三条臂共用同一口径：事实类探针（planted）用中性提问口径（只答要点），" +
			"回答里出现关键标识符记 ok、承认无证据记 miss、其余记 wrong；未知项与陷阱探针用保守口径" +
			"（明确允许回答“无法确认”），仍给出具体实体记 hallucination、承认无证据记 ok。" +
			"另对每条产出摘要的臂各跑一次 LLM 审计员（输入为带角色与工具调用参数的原始转写）。",
		PlantedFacts: plantedFacts,
		UnknownProbe: unknownProbe,
		TrapProbes:   trapProbes,
	}

	for _, sh := range shapes {
		h := buildHistory(sh.Rounds, sh.Runes)
		txt := strings.ToLower(messagesToText(h))
		for _, f := range plantedFacts {
			if !strings.Contains(txt, strings.ToLower(f.Keyword)) {
				fmt.Fprintf(os.Stderr, "WARN: planted fact %q missing from history of %s\n", f.ID, sh.Name)
			}
		}
		for _, t := range trapProbes {
			if strings.Contains(txt, strings.ToLower(t.Keyword)) {
				return fmt.Errorf("FATAL: trap entity %q leaked into history of %s", t.Keyword, sh.Name)
			}
		}
		rep.Cases = append(rep.Cases, runCompareCase(ctx, real, sh, triggerTokens, limitProbes))
	}

	rep.Totals.RealModelCalls = len(real.snapshot())
	for _, c := range real.snapshot() {
		rep.Totals.PromptTokens += c.PromptTokens
		rep.Totals.CompletionTok += c.CompletionTok
		rep.Totals.ReasoningTok += c.ReasoningTok
		rep.Totals.TotalTokens += c.TotalTok
	}
	rep.Totals.WallMs = time.Since(start).Milliseconds()
	return writeCompareOutputs(outDir, rep)
}

func writeCompareOutputs(outDir string, rep compareReport) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "results_compare.json"), raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "REPORT_COMPARE.md"), []byte(renderCompareReport(rep)), 0o644)
}

func renderCompareReport(rep compareReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 上下文压缩「前后幻觉」三臂对照报告（feat-compact，真实模型）\n\n")
	fmt.Fprintf(&b, "- 生成时间：%s\n", rep.GeneratedAt)
	fmt.Fprintf(&b, "- 模型：`%s` @ `%s`（真实网络调用）\n", rep.Model, rep.BaseURL)
	fmt.Fprintf(&b, "- 对照臂：\n")
	for _, a := range rep.Arms {
		fmt.Fprintf(&b, "  - `%s` —— %s\n", a.ID, a.Label)
	}
	fmt.Fprintf(&b, "- token 口径：%s\n", rep.EstimateNote)
	fmt.Fprintf(&b, "- 判定口径：%s\n", rep.VerdictNote)
	fmt.Fprintf(&b, "- 案例：")
	for i, sh := range rep.Shapes {
		if i > 0 {
			fmt.Fprintf(&b, "、")
		}
		fmt.Fprintf(&b, "`%s`（%d 轮 × %d runes）", sh.Name, sh.Rounds, sh.Runes)
	}
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "- 复现：`go run ./eval/compact/hallucination -compare -config <agent_code_local.yml>`\n")
	fmt.Fprintf(&b, "- 原始明细：`eval/compact/hallucination/results_compare.json`（本报告的每个数字都能在其中找到出处）\n\n")

	armIDs := make([]string, 0, len(rep.Arms))
	for _, a := range rep.Arms {
		armIDs = append(armIDs, a.ID)
	}

	// 1. 上下文构成与压缩成效
	fmt.Fprintf(&b, "## 1. 压缩成效与上下文构成\n\n")
	fmt.Fprintf(&b, "| 案例 | 臂 | 触发 | 消息数 前→后 | 估算token 前→后 | 压缩比 | system | 摘要 | user | assistant | tool |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		for _, o := range c.Arms {
			fmt.Fprintf(&b, "| %s | %s | %v | %d→%d | %d→%d | %.1f%% | %d | %d | %d | %d | %d |\n",
				c.Name, o.ID, o.Triggered, o.Before.Messages, o.After.Messages,
				o.Before.Tokens, o.After.Tokens, o.ReductionPct,
				o.Shape.System, o.Shape.Summary, o.Shape.User, o.Shape.Assistant, o.Shape.Tool)
		}
	}
	fmt.Fprintf(&b, "\n（`full` 臂不压缩，因此三列与 `before` 相同；`structured` 会保留 system + 1 条摘要 + 最近 %d 条原文，\n", compress.DefaultKeepRecent)
	fmt.Fprintf(&b, "这正是「最近原文不进摘要模型」在产物上的可见形态。）\n\n")

	// 2. 摘要文本层
	fmt.Fprintf(&b, "## 2. 摘要文本层（不依赖模型再判断）\n\n")
	fmt.Fprintf(&b, "| 案例 | 臂 | 摘要runes | 可验证事实保留 | 未知项仍在 | 陷阱实体被写入 | 含 system 约束词 | 含摘要指令原文 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		for _, o := range c.Arms {
			if o.Strategy == "" {
				continue
			}
			var leaked []string
			for id, v := range o.TrapMention {
				if v {
					leaked = append(leaked, id)
				}
			}
			sort.Strings(leaked)
			fmt.Fprintf(&b, "| %s | %s | %d | %.0f%% | %v | %s | %v | %v |\n",
				c.Name, o.ID, o.SummaryRunes, o.RetentionPct, o.UnknownKept,
				orDash(strings.Join(leaked, ", ")), o.EchoesSystem, o.EchoesInstruction)
		}
	}
	fmt.Fprintf(&b, "\n「含 system 约束词」「含摘要指令原文」是**关键词存在性检查**（不是语义判分）：\n")
	fmt.Fprintf(&b, "前者查摘要里有没有出现 system 约束的特征词，后者查摘要有没有把摘要指令的句子原样搬进去。\n\n")

	// 3. LLM 审计员
	fmt.Fprintf(&b, "## 3. LLM 审计员：矛盾 / 编造 / 遗漏（以原始历史为唯一依据）\n\n")
	fmt.Fprintf(&b, "| 案例 | 臂 | 矛盾条数 | 编造条数 | 遗漏条数 | 审计延迟 | 解析错误 |\n|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		for _, o := range c.Arms {
			j, ok := c.Judges[o.ID]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d ms | %s |\n",
				c.Name, o.ID, len(j.Contradictions), len(j.Fabrications),
				len(j.Omissions), j.LatencyMs, orDash(j.ParseError))
		}
	}
	for _, c := range rep.Cases {
		for _, o := range c.Arms {
			j, ok := c.Judges[o.ID]
			if !ok || (len(j.Contradictions) == 0 && len(j.Fabrications) == 0) {
				continue
			}
			fmt.Fprintf(&b, "\n**%s / %s 的审计明细**\n\n", c.Name, o.ID)
			for _, it := range j.Contradictions {
				fmt.Fprintf(&b, "- 矛盾：%s ← %s\n", oneLine(it.Quote), oneLine(it.Why))
			}
			for _, it := range j.Fabrications {
				fmt.Fprintf(&b, "- 编造：%s ← %s\n", oneLine(it.Quote), oneLine(it.Why))
			}
		}
	}

	// 4. 逐探针三臂对照
	fmt.Fprintf(&b, "\n## 4. 端到端探针三臂对照\n\n")
	fmt.Fprintf(&b, "| 案例 | 探针 | 类型 |")
	for _, id := range armIDs {
		fmt.Fprintf(&b, " %s |", id)
	}
	fmt.Fprintf(&b, " 编造的标识符 |\n|---|")
	for range armIDs {
		fmt.Fprintf(&b, "---|")
	}
	fmt.Fprintf(&b, "---|---|\n")
	for _, c := range rep.Cases {
		for _, p := range c.Probes {
			fmt.Fprintf(&b, "| %s | %s | %s |", c.Name, p.ID, p.Type)
			for _, id := range armIDs {
				fmt.Fprintf(&b, " %s |", p.Verdicts[id])
			}
			inv := ""
			var parts []string
			for _, id := range armIDs {
				if m := p.Invented[id]; m != "" {
					parts = append(parts, id+"="+m)
				}
			}
			inv = orDash(strings.Join(parts, ", "))
			fmt.Fprintf(&b, " %s |\n", inv)
		}
	}

	// 5. 计数汇总
	fmt.Fprintf(&b, "\n## 5. 计数汇总（按臂）\n\n")
	fmt.Fprintf(&b, "| 案例 | 臂 | 事实答对 | 未知项答对 | 陷阱抵抗 | 陷阱编造 | 幻觉总数 | 答错/失忆 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|\n")
	for _, c := range rep.Cases {
		for _, id := range armIDs {
			ct := c.Counts[id]
			fmt.Fprintf(&b, "| %s | %s | %d/%d | %d/%d | %d/%d | %d | %d | %d |\n",
				c.Name, id,
				ct["planted_ok"], ct["planted_total"],
				ct["unknown_ok"], ct["unknown_total"],
				ct["trap_ok"], ct["trap_total"],
				ct["trap_hallucination"], ct["hallucination"], ct["loss"])
		}
	}

	// 6. 真实用量
	fmt.Fprintf(&b, "\n## 6. 真实用量合计\n\n")
	fmt.Fprintf(&b, "- 真实模型调用次数：%d\n", rep.Totals.RealModelCalls)
	fmt.Fprintf(&b, "- prompt tokens：%d\n", rep.Totals.PromptTokens)
	fmt.Fprintf(&b, "- completion tokens：%d（其中 reasoning %d）\n", rep.Totals.CompletionTok, rep.Totals.ReasoningTok)
	fmt.Fprintf(&b, "- total tokens：%d\n", rep.Totals.TotalTokens)
	fmt.Fprintf(&b, "- 墙钟耗时：%d ms\n", rep.Totals.WallMs)

	// 7. 限制
	fmt.Fprintf(&b, "\n## 7. 怎么读这些数（限制与已排除的误判）\n\n")
	fmt.Fprintf(&b, "**（1）判定是关键词 / 标记词匹配，不是语义判分。**\n"+
		"先看有没有「无证据」标记词，有标记词即记 ok；只有既无标记词、又给出具体实体时才记 hallucination。\n"+
		"三条臂用同一套规则，不存在对某条臂更宽松的情况。回答原文全部保留在 results_compare.json，\n"+
		"可按需人工复核。\n\n")
	fmt.Fprintf(&b, "**（2）LLM 审计员的口径依赖它看到的转写，且审计范围与摘要范围对齐。**\n"+
		"审计输入是带角色与 [tool_call args=...] 的原始转写，工具名与参数不会被丢掉。\n"+
		"`legacy` 的摘要覆盖整段历史，因此拿完整转写当唯一依据；`structured` 只把【较早的部分】交给摘要模型，\n"+
		"system 与最近 keep_recent 条原文按设计不进摘要，因此审计员只拿**被摘要的那一段**当依据——\n"+
		"否则最近几轮才出现的事实会被误记成摘要的遗漏或矛盾。\n\n")
	fmt.Fprintf(&b, "**（3）样本量小、历史是合成的。**\n"+
		"每个案例每类探针只有 1～5 条，且长工具链是构造出来的（占位文件正文 + 中段埋点），不是线上真实会话。\n"+
		"计数受单次波动影响，不足以给出比例上的置信区间。\n\n")
	fmt.Fprintf(&b, "**（4）比较的是「同一条历史在三种上下文下的回答」，不是三次独立会话。**\n"+
		"同一份探针文本与提问口径被复用到三条臂，臂间差异来自上下文，而不是提问差异。\n")
	return b.String()
}

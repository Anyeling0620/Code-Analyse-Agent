package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 报告块的起止标记：重复运行只替换标记之间的内容，避免反复追加。
const (
	reportBeginMarker = "<!-- BEGIN AUTO:arm-comparison -->"
	reportEndMarker   = "<!-- END AUTO:arm-comparison -->"
)

// writeReportSection 把本轮各 arm 的对照表写入/更新到 markdown 文件。
// 没有标记块时追加到文件末尾；已有标记块时原地替换。
func writeReportSection(path string, res *Results, specs []armSpec) error {
	block := buildReportBlock(res, specs)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(existing)
	begin := strings.Index(content, reportBeginMarker)
	end := strings.Index(content, reportEndMarker)
	switch {
	case begin >= 0 && end > begin:
		end += len(reportEndMarker)
		content = content[:begin] + block + content[end:]
	case strings.TrimSpace(content) == "":
		content = block + "\n"
	default:
		content = strings.TrimRight(content, "\n") + "\n\n" + block + "\n"
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// buildReportBlock 生成一段 markdown：汇总表 + 分难度拆分 + 跳过说明。
func buildReportBlock(res *Results, specs []armSpec) string {
	var b strings.Builder
	b.WriteString(reportBeginMarker + "\n")
	b.WriteString("## 附：候选池 / RRF-only / 查询改写 对照（自动生成）\n\n")
	fmt.Fprintf(&b, "- 生成时间：%s\n", res.GeneratedAt)
	fmt.Fprintf(&b, "- collection：`%s`，题目数：%d\n", res.Collection, res.Queries)
	fmt.Fprintf(&b, "- 候选池：dense_top_k=%d / sparse_top_k=%d / candidate_k=%d，重排后保留 final=%d 条\n",
		res.Pool.DenseTopK, res.Pool.SparseTopK, res.Pool.CandidateK, res.Pool.FinalK)
	if res.RewriteArmModel != "" {
		fmt.Fprintf(&b, "- 查询改写臂模型：%s\n", res.RewriteArmModel)
	}
	b.WriteString("\n| arm | F@1 | F@5 | F@10 | F@20 | MRR | avg_ms | p50_ms | p95_ms | rw_ms |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, s := range specs {
		sum := res.Summary[s.name]
		if sum == nil {
			continue
		}
		if sum.Queries == 0 && sum.Skipped != "" {
			fmt.Fprintf(&b, "| `%s` | — | — | — | — | — | — | — | — | — |\n", s.name)
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %.4f | %.4f | %.4f | %.4f | %.4f | %.1f | %.1f | %.1f | %.1f |\n",
			s.name,
			sum.FileRecallAt["1"], sum.FileRecallAt["5"], sum.FileRecallAt["10"], sum.FileRecallAt["20"],
			sum.MRR, sum.AvgLatencyMS, sum.P50LatencyMS, sum.P95LatencyMS, sum.AvgRewriteMS)
	}
	b.WriteString("\nF = 文件级 Recall；rw_ms = 该臂平均改写耗时（0 表示该臂不涉及改写）。\n")
	b.WriteString("\n> 符号级 `S@` 指标已废弃并从本表移除：其判定为 " +
		"`symbol == want || strings.Contains(doc.Content, want)`，不限定命中所在文件，" +
		"会产生 `sparse_zh` 的 S@1=1.0 而 F@1=0.0 这类自相矛盾的数。请只看 F@ 与 MRR。\n")

	for _, s := range specs {
		sum := res.Summary[s.name]
		if sum != nil && sum.Queries == 0 && sum.Skipped != "" {
			fmt.Fprintf(&b, "\n> `%s` 本轮跳过：%s\n", s.name, sum.Skipped)
		}
	}

	if len(res.SummaryByDifficulty) > 0 {
		diffs := make([]string, 0, len(res.SummaryByDifficulty))
		for d := range res.SummaryByDifficulty {
			diffs = append(diffs, d)
		}
		sort.Strings(diffs)
		b.WriteString("\n### 分难度（文件级 F@5 / MRR）\n\n")
		b.WriteString("| difficulty | arm | n | F@5 | MRR |\n| --- | --- | --- | --- | --- |\n")
		for _, d := range diffs {
			for _, s := range specs {
				sum := res.SummaryByDifficulty[d][s.name]
				if sum == nil || sum.Queries == 0 {
					continue
				}
				fmt.Fprintf(&b, "| %s | `%s` | %d | %.4f | %.4f |\n",
					d, s.name, sum.Queries, sum.FileRecallAt["5"], sum.MRR)
			}
		}
	}

	fmt.Fprintf(&b, "\n_（本节由 `go run ./eval/rag -report eval/rag/REPORT.md` 自动生成于 %s，重跑即覆盖。）_\n",
		time.Now().Format(time.RFC3339))
	b.WriteString(reportEndMarker)
	return b.String()
}

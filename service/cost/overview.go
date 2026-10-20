package cost

import (
	"context"
	"math"
	"sort"
	"time"

	"edu.agent.code/service/do"
	"edu.agent.code/service/dto"
	"edu.agent.code/utils/logger"
)

// directToolKey 是空 tool_name 的展示名：这类记录是用户直接提问、没有经过工具调用，
// 归成一个伪工具而不是丢弃，否则分项之和会对不上总量。
const directToolKey = "(direct)"

// Overview 返回当前用户 [from, to] 区间（含首含尾）的消耗总览。
func (s *Service) Overview(ctx context.Context, userID string, from, to time.Time) (dto.CostOverview, error) {
	// to 是含尾的自然日，仓储层按 [from, to) 查询，这里 +1 天换成开区间。
	rows, err := s.cost.Overview(ctx, userID, from, to.AddDate(0, 0, 1))
	if err != nil {
		logger.Error("cost.Overview user=%s from=%s to=%s err=%v",
			userID, from.Format(time.DateOnly), to.Format(time.DateOnly), err)
		return dto.CostOverview{}, err
	}
	return buildOverview(from, to, rows), nil
}

// overviewAgg 是聚合过程中的累加器。
type overviewAgg struct {
	label  string
	calls  int64
	tokens int64
	cny    float64
	last   time.Time
}

func (a *overviewAgg) add(label string, tokens int64, cny float64, at time.Time) {
	if a.calls == 0 {
		a.label = label
	}
	a.calls++
	a.tokens += tokens
	a.cny += cny
	if at.After(a.last) {
		a.last = at
	}
}

// buildOverview 在 Go 内完成全部聚合（总量 / 分工具 / 分模型 / 分会话 / 每日）。
//
// 为什么不在 SQL 里 GROUP BY 日期：occurred_at 是带 +08:00 的 ISO 文本，
// SQLite 的 date() 按字面值截断（等价于 UTC 口径），落在本地 00:00-08:00 的记录会被
// 算到前一天，和 /api/cost/daily 的 time.Local 自然日口径对不上。区间上限 366 天、
// 明细走 user_id+occurred_at 索引，一次取回在内存里聚合足够。
//
// daily 必须按区间逐日补齐：前端画的是连续时间轴，缺日会让曲线把两个不相邻的点连起来。
func buildOverview(from, to time.Time, rows []do.CostOverviewRow) dto.CostOverview {
	total := dto.CostOverviewTotal{}
	tools := make(map[string]*overviewAgg)
	models := make(map[string]*overviewAgg)
	sessions := make(map[string]*overviewAgg)

	daily := make([]dto.CostDailyPoint, 0)
	dayIndex := make(map[string]int)
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		dayIndex[key] = len(daily)
		daily = append(daily, dto.CostDailyPoint{Date: key})
	}

	for _, row := range rows {
		tokens := row.PromptTokens + row.CompletionTokens
		// 未命中 token 与 DailyUsage 保持一致，用 prompt-cached 推导，
		// 加缓存列之前写入的历史记录 cache_miss_tokens 为 0，直接取列会漏算。
		miss := row.PromptTokens - row.CachedTokens
		if miss < 0 {
			miss = 0
		}

		total.Calls++
		total.Tokens += tokens
		total.PromptTokens += row.PromptTokens
		total.CompletionTokens += row.CompletionTokens
		total.CacheHitTokens += row.CachedTokens
		total.CacheMissTokens += miss
		total.CNY += row.EstimatedCNY
		total.CacheHitCNY += row.CacheHitCNY
		total.CacheMissCNY += row.CacheMissCNY

		toolKey := row.ToolName
		if toolKey == "" {
			toolKey = directToolKey
		}
		aggInto(tools, toolKey, toolKey, tokens, row.EstimatedCNY, row.OccurredAt)
		aggInto(models, row.Model, row.Model, tokens, row.EstimatedCNY, row.OccurredAt)
		// 没有 session_id 的记录（不经过会话的直调）不算会话，也不进分会话列表：
		// 前端拿 key 做跳转，空 key 只会渲染出一个点不动的空行。
		if row.SessionID != "" {
			aggInto(sessions, row.SessionID, row.SessionTitle, tokens, row.EstimatedCNY, row.OccurredAt)
		}

		// occurred_at 按本地时区归日，与区间端点（time.Local 的零点）同口径。
		if idx, ok := dayIndex[row.OccurredAt.In(time.Local).Format(time.DateOnly)]; ok {
			daily[idx].Calls++
			daily[idx].Tokens += tokens
			daily[idx].CNY += row.EstimatedCNY
		}
	}

	total.Sessions = int64(len(sessions))
	total.Tools = int64(len(tools))
	total.Models = int64(len(models))

	return dto.CostOverview{
		From:      from.Format(time.DateOnly),
		To:        to.Format(time.DateOnly),
		Total:     total,
		ByTool:    toOverviewItems(tools, total.CNY),
		ByModel:   toOverviewItems(models, total.CNY),
		BySession: toOverviewItems(sessions, total.CNY),
		Daily:     daily,
	}
}

func aggInto(m map[string]*overviewAgg, key, label string, tokens int64, cny float64, at time.Time) {
	a, ok := m[key]
	if !ok {
		a = &overviewAgg{}
		m[key] = a
	}
	a.add(label, tokens, cny, at)
}

// toOverviewItems 把累加结果转成按成本降序的列表；成本相同时按 key 升序，
// 保证同一份数据每次返回顺序一致（map 遍历本身无序）。
func toOverviewItems(m map[string]*overviewAgg, totalCNY float64) []dto.CostOverviewItem {
	items := make([]dto.CostOverviewItem, 0, len(m))
	for key, a := range m {
		share := 0.0
		if totalCNY > 0 {
			share = math.Round(a.cny/totalCNY*10000) / 10000
		}
		items = append(items, dto.CostOverviewItem{
			Key:    key,
			Label:  a.label,
			Calls:  a.calls,
			Tokens: a.tokens,
			CNY:    a.cny,
			Share:  share,
			LastAt: a.last.In(time.Local).Format(time.RFC3339),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CNY != items[j].CNY {
			return items[i].CNY > items[j].CNY
		}
		return items[i].Key < items[j].Key
	})
	return items
}

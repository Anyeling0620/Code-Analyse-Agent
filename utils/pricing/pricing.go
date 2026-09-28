package pricing

import "github.com/shopspring/decimal"

// Rate 描述单个模型的单价，单位为「元 / 百万 token」。
type Rate struct {
	Prompt     float64 // 输入单价，适用于缓存未命中的 prompt token
	CacheHit   float64 // 缓存命中单价，适用于命中 prompt cache 的 token
	Completion float64 // 输出单价
}

// Breakdown 是同一笔用量按 token 桶拆开的成本（单位：元）。
type Breakdown struct {
	CacheHitCNY   float64 // 命中 prompt cache 的输入部分
	CacheMissCNY  float64 // 未命中的输入部分
	CompletionCNY float64 // 输出部分
}

// TotalCNY 返回三段之和。
func (b Breakdown) TotalCNY() float64 {
	total, _ := decimal.NewFromFloat(b.CacheHitCNY).
		Add(decimal.NewFromFloat(b.CacheMissCNY)).
		Add(decimal.NewFromFloat(b.CompletionCNY)).Float64()
	return total
}

// Calculate 按缓存命中 / 未命中分别计价，并返回拆分后的金额。
// prompt 为总输入 token（含命中部分），cached 为其中命中 prompt cache 的 token 数。
// 命中部分按 rate.CacheHit 计价，其余输入按 rate.Prompt 计价，避免统一按输入价高估成本。
func Calculate(rate Rate, prompt, cached, completion int64) Breakdown {
	if prompt < 0 {
		prompt = 0
	}
	if cached < 0 {
		cached = 0
	}
	if cached > prompt { // 命中数不可能超过总输入，防御上游统计异常
		cached = prompt
	}
	if completion < 0 {
		completion = 0
	}
	if prompt <= 0 && completion <= 0 {
		return Breakdown{}
	}

	missed := prompt - cached
	missedValue := tokenCost(missed, rate.Prompt)
	cachedValue := tokenCost(cached, rate.CacheHit)
	completionValue := tokenCost(completion, rate.Completion)

	hitCNY, _ := cachedValue.Float64()
	missCNY, _ := missedValue.Float64()
	completionCNY, _ := completionValue.Float64()
	return Breakdown{
		CacheHitCNY:   hitCNY,
		CacheMissCNY:  missCNY,
		CompletionCNY: completionCNY,
	}
}

// tokenCost 计算指定 token 数在给定单价下的费用（元）。
func tokenCost(tokens int64, price float64) decimal.Decimal {
	if tokens <= 0 || price == 0 {
		return decimal.Zero
	}
	return decimal.NewFromInt(tokens).Mul(decimal.NewFromFloat(price)).Div(decimal.NewFromInt(1000000))
}

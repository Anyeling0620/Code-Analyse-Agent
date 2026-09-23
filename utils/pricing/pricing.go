package pricing

import "github.com/shopspring/decimal"

type Rate struct {
	Prompt     float64
	Completion float64
}

func CalculateCNY(rate Rate, prompt, completion int64) float64 {
	if prompt < 0 {
		prompt = 0.0
	}
	if completion < 0 {
		completion = 0.0
	}
	if prompt <= 0 && completion <= 0 {
		return 0.0
	}
	promptValue := decimal.NewFromInt(prompt).Mul(decimal.NewFromFloat(rate.Prompt)).Div(decimal.NewFromInt(1000000))
	completionValue := decimal.NewFromInt(completion).Mul(decimal.NewFromFloat(rate.Completion)).Div(decimal.NewFromInt(1000000))
	totalValue, _ := promptValue.Add(completionValue).Float64()
	return totalValue
}

package dto

type QuotaToday struct {
	UserID string `json:"user_id"`
	Plan   string `json:"plan"`
	Date   string `json:"date"`
	Used   int64  `json:"used"`
	Limit  int    `json:"limit"`
}

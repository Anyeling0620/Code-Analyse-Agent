package dto

type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
	TraceID string `json:"trace_id"`
}

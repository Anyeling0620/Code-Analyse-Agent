package dto

type Pager struct {
	Page  int `json:"page" form:"page"`
	Limit int `json:"limit" form:"limit"`
}

func (p *Pager) GetPage() int {
	if p == nil {
		return 0
	}
	if p.Page == 0 {
		return 1
	}
	return p.Page

}

func (p *Pager) GetLimit() int {
	return p.Limit
}

func (p *Pager) GetOffset() int {
	if p.Page == 0 {
		p.Page = 1
	}
	if p.Limit == 0 || p.Limit > 100 {
		p.Limit = 50
	}
	return (p.Page - 1) * p.Limit
}

type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
	TraceID string `json:"trace_id"`
}

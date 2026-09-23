package api

import (
	"edu.agent.code/adaptor"
	"edu.agent.code/service/cost"
	"edu.agent.code/service/quota"
)

type Handler struct {
	adaptor adaptor.IAdaptor
	cost    *cost.Service
	quota   *quota.Service
}

func NewHandler(adaptor adaptor.IAdaptor) *Handler {
	return &Handler{
		adaptor: adaptor,
		cost:    cost.NewService(adaptor),
		quota:   quota.NewService(adaptor),
	}
}

func (h *Handler) GetQuotaService() *quota.Service {
	return h.quota
}

package api

import (
	"edu.agent.code/adaptor"
	"edu.agent.code/service/cost"
)

type Handler struct {
	adaptor adaptor.IAdaptor
	cost    *cost.Service
}

func NewHandler(adaptor adaptor.IAdaptor) *Handler {
	return &Handler{
		adaptor: adaptor,
		cost:    cost.NewService(adaptor),
	}
}

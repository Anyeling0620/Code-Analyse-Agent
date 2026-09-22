package api

import "edu.agent.code/adaptor"

type Handler struct {
	adaptor adaptor.IAdaptor
}

func NewHandler(adaptor adaptor.IAdaptor) *Handler {
	return &Handler{
		adaptor: adaptor,
	}
}

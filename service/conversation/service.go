package conversation

import (
	"edu.agent.code/adaptor"
	"edu.agent.code/adaptor/repo/session"
)

type Service struct {
	adaptor adaptor.IAdaptor
	session *session.Session
}

func NewService(adaptor adaptor.IAdaptor) *Service {
	return &Service{
		adaptor: adaptor,
		session: session.NewSession(adaptor),
	}
}

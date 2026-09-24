package rag

type Service struct {
}

func NewService() *Service {
	return &Service{}
}

func (s *Service) Close() error {
	return nil
}

package conversation

import (
	"context"
	"edu.agent.code/service/dto"
)

type ChatEmit func(event dto.ChatStreamEvent) error

func (s *Service) ChatStream(
	ctx context.Context,
	req dto.ChatRequest,
	emit ChatEmit) error {
	return nil
}

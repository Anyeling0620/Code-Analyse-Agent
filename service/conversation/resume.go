package conversation

import (
	"context"
	"edu.agent.code/service/dto"
)

// ResumeChat 审批恢复的兼容入口。
//
// 与 ChatStream 一样，这里只负责"起 run + 转发事件"：真正的恢复执行在
// executeResumeRun 里跑，事件仍然落回被中断的那条 run，客户端可以用同一个
// 事件游标继续读。
func (s *Service) ResumeChat(ctx context.Context, req *dto.ChatResumeRequest, emit ChatEmit) error {
	runID, _, afterSeq, err := s.StartResumeRun(ctx, req)
	if err != nil {
		return err
	}
	return s.PumpRun(ctx, runID, afterSeq, emit)
}

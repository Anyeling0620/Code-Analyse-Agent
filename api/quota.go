package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
	"time"
)

func (h *Handler) QuotaToday(ctx *gin.Context) {
	user := h.authUser(ctx)
	today := time.Now().Format(time.DateOnly)
	used, err := h.quota.Today(ctx.Request.Context(), user.UserID, today)
	if err != nil {
		h.writeResp(ctx, nil, common.ServerError.WithError(err))
		return
	}
	resp := &dto.QuotaToday{
		UserID: user.UserID,
		Plan:   string(user.Plan),
		Date:   today,
		Used:   used,
		Limit:  user.Plan.DailyQuota(),
	}
	h.writeResp(ctx, resp, common.OK)
}

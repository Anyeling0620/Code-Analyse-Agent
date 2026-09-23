package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
	"time"
)

func (h *Handler) CostDaily(c *gin.Context) {
	day, err := parseDay(c.Query("date"), time.Now())
	if err != nil {
		h.writeResp(c, nil, common.ParamError.WithError(err))
		return
	}

	cny, err := h.cost.DailyTotal(c.Request.Context(), day)
	if err != nil {
		h.writeResp(c, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(c, dto.CostDailyTotal{
		Date: day.Format(time.DateOnly),
		CNY:  cny,
	}, common.OK)

}

func (h *Handler) CostByUser(c *gin.Context) {
	from, to, err := parseRange(c)
	if err != nil {
		h.writeResp(c, nil, common.ParamError.WithError(err))
		return
	}
	items, err := h.cost.GroupByUser(c.Request.Context(), from, to)
	if err != nil {
		h.writeResp(c, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(c, items, common.OK)
}
func (h *Handler) CostByTool(c *gin.Context) {
	from, to, err := parseRange(c)
	if err != nil {
		h.writeResp(c, nil, common.ParamError.WithError(err))
		return
	}
	items, err := h.cost.GroupByTool(c.Request.Context(), from, to)
	if err != nil {
		h.writeResp(c, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(c, items, common.OK)
}

func parseDay(row string, fallback time.Time) (time.Time, error) {
	if row == "" {
		return fallback, nil
	}
	return time.ParseInLocation(time.DateOnly, row, time.Local)
}

func parseRange(ctx *gin.Context) (time.Time, time.Time, error) {
	now := time.Now()
	from, err := parseDay(ctx.Query("from"), now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseDay(ctx.Query("to"), now.AddDate(0, 0, 1))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from, to, nil
}

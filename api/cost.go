package api

import (
	"edu.agent.code/common"
	"edu.agent.code/service/dto"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

func (h *Handler) CostDaily(c *gin.Context) {
	day, err := parseDay(c.Query("date"), time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, dto.Response[any]{
			Code:    common.ParamError.Code,
			Message: err.Error(),
			Data:    nil,
		})
		return
	}

	cny, err := h.cost.DailyTotal(c.Request.Context(), day)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Response[any]{
			Code:    common.DatabaseError.Code,
			Message: err.Error(),
			Data:    nil,
			TraceID: c.GetString("trace_id"),
		})
		return
	}
	c.JSON(http.StatusOK, dto.Response[dto.CostDailyTotal]{
		Code:    common.OK.Code,
		Message: common.OK.Msg,
		Data: dto.CostDailyTotal{
			Date: day.Format(time.DateOnly),
			CNY:  cny,
		},
		TraceID: c.GetString("trace_id"),
	})

}

func parseDay(ray string, fallback time.Time) (time.Time, error) {
	if ray == "" {
		return fallback, nil
	}
	return time.ParseInLocation(time.DateOnly, ray, time.Local)
}

package api

import (
	"edu.agent.code/common"
	"fmt"
	"github.com/gin-gonic/gin"
	"time"
)

func (h *Handler) CostDaily(c *gin.Context) {
	day, err := parseDay(c.Query("date"), time.Now())
	if err != nil {
		h.writeResp(c, nil, common.ParamError.WithError(err))
		return
	}

	user := h.authUser(c)
	item, err := h.cost.DailyUsage(c.Request.Context(), user.UserID, day)
	if err != nil {
		h.writeResp(c, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(c, item, common.OK)

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

// CostOverview 返回当前登录用户在一段日期内的个人消耗总览，忽略任何 user_id 参数：
// 成本数据按账号记账，能看谁的区间应该只由登录态决定。
func (h *Handler) CostOverview(c *gin.Context) {
	from, to, err := parseOverviewRange(c.Query("from"), c.Query("to"), time.Now())
	if err != nil {
		h.writeResp(c, nil, common.ParamError.WithError(err))
		return
	}
	user := h.authUser(c)
	if user == nil {
		h.writeResp(c, nil, common.AuthError)
		return
	}
	overview, err := h.cost.Overview(c.Request.Context(), user.UserID, from, to)
	if err != nil {
		h.writeResp(c, nil, common.ServerError.WithError(err))
		return
	}
	h.writeResp(c, overview, common.OK)
}

// parseOverviewRange 解析总览区间，默认「今天往前 29 天 ~ 今天」，两端都是含尾的自然日。
// 上限 366 天：明细要一次性读进内存聚合，不设上限时一个超大区间会把整库记录都拉出来。
func parseOverviewRange(rawFrom, rawTo string, now time.Time) (time.Time, time.Time, error) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from, err := parseDay(rawFrom, today.AddDate(0, 0, -29))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseDay(rawTo, today)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if from.After(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("from 不能晚于 to")
	}
	// 含首含尾 366 天对应两端相差 365 天。
	if to.Sub(from) > 365*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("日期跨度不能超过 366 天")
	}
	return from, to, nil
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

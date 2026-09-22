package api

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "OK"})
}

package api

import (
	"fmt"
	"github.com/gin-gonic/gin"
)

func (h *Handler) Chat(c *gin.Context) {
	fmt.Println("chat finish")
	return
}

package api

import (
	"fmt"
	"github.com/gin-gonic/gin"
)

func (h *Handler) ChatCompletion(c *gin.Context) {
	fmt.Println("chat finish")
	return
}

func (h *Handler) ChatStream(c *gin.Context) {
	fmt.Println("stream finish")
	return
}

func (h *Handler) ChatResume(c *gin.Context) {
	fmt.Println("resume finish")
	return
}

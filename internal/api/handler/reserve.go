package handler

import (
	"errors"
	"net/http"

	"aegis-gateway/internal/service"

	"github.com/gin-gonic/gin"
)

// Data Transfer Object
type ReserveRequest struct {
	// binding :"required" is the validator engine integrated at the core of Gin
	// 面试防坑：必须对数字类型做边界校验，如 gt=0 (大于0)，防止恶意透传负数或 0 击穿底层逻辑
	UserID     string `json:"user_id" binding:"required,min=5"`
	ResourceID int64  `json:"resource_id" binding:"required,gt=0"`
}

// A unified booking portal exposed to the front end
func HandleReserve(c *gin.Context) {
	var req ReserveRequest

	// 【面试考点】：为什么用 ShouldBindJSON 而不是 BindJSON？
	// BindJSON 在校验失败时会在底层强行写入 HTTP 400 状态码并终止请求，你无法自定义错误返回格式。
	// ShouldBindJSON 把控制权交还给开发者，我们可以封装统一的 JSON 错误协议。
	//gin.H = map[string]interface{} 的简写，用来快速拼一个 key-value 集合给 c.JSON() 序列化输出
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":  400,
			"error": "Invalid request parameters:" + err.Error(),
		})
		return
	}

	//hander接service
	err := service.Reserve(c.Request.Context(), req.UserID, req.ResourceID)

	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"code": 200, "msg": "queued"})
	case errors.Is(err, service.ErrSoldOut):
		c.JSON(http.StatusGone, gin.H{"code": 410, "msg": "sold out"})
	case errors.Is(err, service.ErrAlreadyReserved):
		c.JSON(http.StatusConflict, gin.H{"code": 409, "error": "already reserved"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "error": "Internal server error, please try again later"})
	}
}

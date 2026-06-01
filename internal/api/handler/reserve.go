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
	UserID     string `json:"user_id" binding:"required,min=5"`
	ResourceID int64  `json:"resource_id" binding:"required,gt=0"`
}

// A unified booking portal exposed to the front end
func HandleReserve(c *gin.Context) {
	var req ReserveRequest

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

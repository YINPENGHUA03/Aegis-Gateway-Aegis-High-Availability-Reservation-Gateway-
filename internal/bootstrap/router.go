package bootstrap

import (
	"aegis-gateway/internal/api/handler"
	"aegis-gateway/internal/api/middleware"
	"aegis-gateway/internal/global"
	"net/http"
	"os"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// SetupRouter Configure and return a configured Gin engine
func SetupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()           // ← 不要用 Default()
	r.SetTrustedProxies(nil) // 不读 X-Forwarded-For，直接用 TCP IP
	r.Use(gin.Recovery())
	pprof.Register(r)

	// 防并发中间件 (Middleware) 全局只挂IP限流
	// 根据 APP_MODE 决定中间件强度
	if os.Getenv("APP_MODE") == "loadtest" {
		r.Use(middleware.IPRateLimitMiddleware(rate.Inf, 1000)) // 不限流
	} else {
		r.Use(middleware.IPRateLimitMiddleware(rate.Limit(5), 10)) // 生产
	}
	// API group
	v1 := r.Group("/api/v1")
	{
		v1.GET("/ping", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"status": "Aegis Gateway Online",
				"mysql":  "connected",
				"redis":  "connected",
			})
		})
		//Registration route
		v1.POST("/reserve", middleware.AntiSpamMiddleware(global.Redis), handler.HandleReserve)
		//v1.POST("/reserve", handler.HandleReserve)
	}

	return r
}

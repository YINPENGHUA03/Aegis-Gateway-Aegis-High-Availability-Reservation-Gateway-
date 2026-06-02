package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"aegis-gateway/pkg/distributed_lock"
)

// TTL 保证锁最多 2 秒就自动消失，不会把用户永久锁死。
// 这层中间件牺牲了一丁点极端情况下的用户重试体验，
// 换取系统核心逻辑（Lua 脚本和数据库）在高并发下的安全。
func AntiSpamMiddleware(rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		//HTTP 规范里，自定义的请求头约定以 X- 开头，表示"这是非标准的、应用自己定义的字段"
		userID := c.GetHeader("X-User-ID")
		if userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":  401,
				"error": "Unauthorised: X-User-ID request header is missing",
			})
			c.Abort()
			return
		}

		//Attempting to acquire a lock in Redis
		ctx := c.Request.Context()
		lockKey := "lock:appoint:" + userID
		lock := distributed_lock.New(rdb, lockKey, 2*time.Second)

		ok, err := lock.TryLock(ctx)
		if err != nil {
			// Redis 崩溃时的降级策略：这里选择 Fail-Closed (拒绝服务)，保护底层数据库
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "The system is currently busy. Please try again later."})
			c.Abort()
			return
		}
		if !ok {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"code":  429,
				"error": "You have performed this action too many times. Please wait 2 seconds before trying again.",
			})
			c.Abort()
			return
		}

		defer lock.Unlock(ctx)
		c.Next()
	}
}

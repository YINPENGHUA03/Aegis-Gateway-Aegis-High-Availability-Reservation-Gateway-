package repository

import (
	"context"
	"errors"
	"strconv"

	"aegis-gateway/internal/global"
)

// ReserveStock 调用 reserve.lua，在 Redis 端原子完成「库存扣减 + 用户去重」。
// 把 keys 拼接和 EvalSha 收敛在 repository 层，service 层只关心返回码的业务含义。
//
// 返回值约定（与 reserve.lua 的返回值一一对应）：
//
//	 1  → 抢购成功（库存已扣减，userID 已写入去重集合）
//	 0  → 库存售罄
//	-1  → 该用户已抢过（重复请求）
func ReserveStock(ctx context.Context, userID string, resourceID int64) (int64, error) {
	// strconv.FormatInt 把 int64 转成字符串，拼出两个 Redis key。
	key1 := "resource:stock:" + strconv.FormatInt(resourceID, 10)
	key2 := "resource:buyers:" + strconv.FormatInt(resourceID, 10)
	keys := []string{key1, key2}

	result, err := global.Redis.EvalSha(ctx, global.ReserveSHA, keys, userID).Result()
	if err != nil {
		return 0, err
	}

	// EvalSha 返回 interface{}，Lua 的 number 在 go-redis 里映射成 int64。
	code, ok := result.(int64)
	if !ok {
		return 0, errors.New("unexpected redis result type")
	}
	return code, nil
}

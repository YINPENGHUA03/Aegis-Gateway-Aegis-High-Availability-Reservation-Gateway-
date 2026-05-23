package repository

import (
	"context"
	"database/sql"
	"errors"

	"aegis-gateway/internal/global"

	"github.com/google/uuid"
)

// Order maps to a row in the t_order table
type Order struct {
	OrderNo    string
	UserID     string
	ResourceID int64
	Status     int // 0=Pending payment  1=Paid  2=Cancelled
}

// generateOrderNo Generate a globally unique order number based on UUID v4.
// UUID v4 是 122 位随机数，冲突概率 2^-122，在分布式多节点场景下无需协调即可保证唯一。
// 缺点：完全随机会让 InnoDB 主键 B+Tree 频繁页分裂；如果对插入性能敏感，可换 Snowflake。
func generateOrderNo() string {
	return "ORD-" + uuid.NewString()
}

// InsertOrder 向 t_order 表插入一条新订单，返回生成的订单号。
func InsertOrder(ctx context.Context, userID string, resourceID int64) (string, error) {
	orderNo := generateOrderNo()
	_, err := global.DB.ExecContext(
		ctx,
		"INSERT INTO t_order (order_no, user_id, resource_id, status) VALUES (?, ?, ?, 0)",
		orderNo,
		userID,
		resourceID,
	)
	if err != nil {
		return "", err
	}

	return orderNo, nil
}

// GetOrderByUserAndResource 按用户+资源查询订单，返回第一条匹配记录。
// 返回值约定：
//   - (*Order, nil)  → 找到了记录
//   - (nil, nil)     → 没找到记录（不是错误，只是不存在）
//   - (nil, err)     → 查询本身出错（网络、超时等）
func GetOrderByUserAndResource(ctx context.Context, userID string, resourceID int64) (*Order, error) {
	var o Order
	row := global.DB.QueryRowContext(
		ctx,
		"SELECT order_no, user_id, resource_id, status FROM t_order WHERE user_id = ? AND resource_id = ? LIMIT 1",
		userID,
		resourceID,
	)

	// Scan 将这一行的列值按顺序写入变量，顺序必须和 SELECT 的列顺序完全一致。
	err := row.Scan(&o.OrderNo, &o.UserID, &o.ResourceID, &o.Status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	return &o, nil
}

func UpdateOrderStatus(ctx context.Context, userID string, resourceID int64) (int64, error) {
	result, err := global.DB.ExecContext(
		ctx,
		"UPDATE t_order SET status = 2 WHERE user_id = ? AND resource_id = ? AND status = 0",
		userID,
		resourceID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

package model

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"gorm.io/gorm/schema"
)

// 本包用两个自定义 serializer 把「Go 侧自然类型」与「库侧既有表示」解耦，
// 实体照常声明 time.Time / bool，落库形态保持与既有库文件逐字节一致。
//
// 为什么不用 gorm 的默认映射：
//   - time.Time 默认按驱动布局写成 `2006-01-02 15:04:05.999999999-07:00`（空格分隔），
//     而事件游标、用量区间过滤都按**字符串比较**时间（`started_at >= ?`）；
//     空格(0x20) < 'T'(0x54)，格式一变区间比较整体错位；
//   - 布尔列必须是 INTEGER 0/1，才能与既有行、与 `WHERE archived = ?` 的整型入参一致。
//
// 建表不由 gorm 负责（见 internal/adapters/outbound/sqlite/store 的迁移内核），因此这里只需保证读写双向适配。
const (
	// serializerTS 是时间列使用的 serializer 名。
	serializerTS = "arkmate_rfc3339"
	// serializerFlag 是布尔列使用的 serializer 名。
	serializerFlag = "arkmate_flag"
)

func init() {
	schema.RegisterSerializer(serializerTS, tsSerializer{})
	schema.RegisterSerializer(serializerFlag, flagSerializer{})
}

// tsSerializer 在 time.Time 与 RFC3339Nano 文本之间转换。
type tsSerializer struct{}

func (tsSerializer) Value(_ context.Context, field *schema.Field, _ reflect.Value, fieldValue any) (any, error) {
	switch v := fieldValue.(type) {
	case time.Time:
		return FormatTime(v), nil
	case *time.Time:
		if v == nil {
			return "", nil
		}
		return FormatTime(*v), nil
	default:
		return nil, fmt.Errorf("model: 列 %s 期望 time.Time，实际 %T", field.DBName, fieldValue)
	}
}

func (tsSerializer) Scan(_ context.Context, field *schema.Field, dst reflect.Value, dbValue any) error {
	switch v := dbValue.(type) {
	case nil:
		return field.Set(context.Background(), dst, time.Time{})
	case string:
		return field.Set(context.Background(), dst, ParseTime(v))
	case []byte:
		return field.Set(context.Background(), dst, ParseTime(string(v)))
	case time.Time:
		// 驱动已自行解析（如经 _time_format 写入的旧行）：直接采用。
		return field.Set(context.Background(), dst, v.UTC())
	default:
		return fmt.Errorf("model: 列 %s 无法解析时间值 %T", field.DBName, dbValue)
	}
}

// flagSerializer 在 bool 与 INTEGER 0/1 之间转换。
type flagSerializer struct{}

func (flagSerializer) Value(_ context.Context, field *schema.Field, _ reflect.Value, fieldValue any) (any, error) {
	switch v := fieldValue.(type) {
	case bool:
		return boolToInt(v), nil
	case *bool:
		if v == nil {
			return int64(0), nil
		}
		return boolToInt(*v), nil
	default:
		return nil, fmt.Errorf("model: 列 %s 期望 bool，实际 %T", field.DBName, fieldValue)
	}
}

func (flagSerializer) Scan(_ context.Context, field *schema.Field, dst reflect.Value, dbValue any) error {
	switch v := dbValue.(type) {
	case nil:
		return field.Set(context.Background(), dst, false)
	case bool:
		return field.Set(context.Background(), dst, v)
	case int64:
		return field.Set(context.Background(), dst, v != 0)
	case int:
		return field.Set(context.Background(), dst, v != 0)
	case []byte:
		return flagScanString(field, dst, string(v))
	case string:
		return flagScanString(field, dst, v)
	default:
		return fmt.Errorf("model: 列 %s 无法解析布尔值 %T", field.DBName, dbValue)
	}
}

func flagScanString(field *schema.Field, dst reflect.Value, s string) error {
	if s == "" {
		return field.Set(context.Background(), dst, false)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("model: 列 %s 无法解析布尔值 %q: %w", field.DBName, s, err)
	}
	return field.Set(context.Background(), dst, n != 0)
}

// FlagValue 把布尔转成列上的 INTEGER 表示。
//
// serializer 只作用于**实体字段**；裸 SQL 的 where/updates map 入参不经过 serializer，
// 必须先自行转换，否则 `WHERE archived = true` 会与库里的 0/1 对不上。
func FlagValue(b bool) int64 { return boolToInt(b) }

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

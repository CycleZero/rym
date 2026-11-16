package biz

import (
	"encoding/hex"

	"github.com/google/uuid"
)

// newMsgID 生成 UUIDv7（hex 编码，32 字符，无连字符）。
//
// 为什么用 UUIDv7 而不是自增 id / 雪花：
//   - msg_id 只需要全局唯一、不需要有序；UUIDv7 时间前缀让排查时"大致按时间排序"
//   - 会话内顺序由单独的 seq 负责，两者职责不混用（文档 §4.1）
//   - 客户端可以先生成、离线排队，服务端不必回填
func newMsgID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

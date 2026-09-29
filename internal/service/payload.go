package service

import "encoding/json"

// encodeEventPayload 把结构化数据编码为 journal 事件载荷，保证罐容量、
// 计划量与实际入罐量等可追溯数据在事件日志中有稳定形态。
func encodeEventPayload(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

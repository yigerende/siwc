package service

import "sync"

// sseScannerBuf64KSize 提高到 1MB：response.completed 事件的 data 行在长会话
// 中（多输出项 + 审核元数据 + usage）可轻松超过 64KB，原 64KB 上限导致
// bufio.Scanner 返回 ErrTooLong 并停止读取，response.completed 中的
// usage/cached_tokens 从未被解析——表现为「缓存时有时无」（TTFT 分析证实
// cache=0 的请求实际命中了缓存，只是 usage 上报丢失）。
const sseScannerBuf64KSize = 1024 * 1024

type sseScannerBuf64K [sseScannerBuf64KSize]byte

var sseScannerBuf64KPool = sync.Pool{
	New: func() any {
		return new(sseScannerBuf64K)
	},
}

func getSSEScannerBuf64K() *sseScannerBuf64K {
	v := sseScannerBuf64KPool.Get()
	buf, ok := v.(*sseScannerBuf64K)
	if !ok || buf == nil {
		return new(sseScannerBuf64K)
	}
	return buf
}

func putSSEScannerBuf64K(buf *sseScannerBuf64K) {
	if buf == nil {
		return
	}
	sseScannerBuf64KPool.Put(buf)
}

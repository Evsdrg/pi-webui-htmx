// Package events 提供有界的事件补发缓冲。
// 设计约束：按 epoch 隔离，环形淘汰，字节与条数双上限，绝不为补发无限增长。
package events

import (
	"sync"
)

// Item 是一条可补发的事件。
type Item struct {
	Seq     uint64
	Payload []byte
}

// Ring 是单个 epoch 内的有界补发缓冲。
type Ring struct {
	mu       sync.Mutex
	items    []Item
	bytes    int64
	maxItems int
	maxBytes int64
	dropped  uint64
}

// NewRing 构造补发缓冲；任一上限非正时使用默认值。
func NewRing(maxItems int, maxBytes int64) *Ring {
	if maxItems <= 0 {
		maxItems = 256
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &Ring{maxItems: maxItems, maxBytes: maxBytes}
}

// Push 追加事件并在超限时淘汰最旧项。
func (r *Ring) Push(seq uint64, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, Item{Seq: seq, Payload: payload})
	r.bytes += int64(len(payload))
	for len(r.items) > r.maxItems || r.bytes > r.maxBytes {
		// 只剩一条时一定是因为它自己就超了字节上限（maxItems 至少为 1，
		// 那时 len > maxItems 不可能成立），淘汰它会让 Ring 变成空的、
		// 看起来像「什么都没有」，反而误导重连方。保留它并记录丢弃。
		if len(r.items) == 1 {
			r.dropped++
			break
		}
		r.bytes -= int64(len(r.items[0].Payload))
		r.items = r.items[1:]
		r.dropped++
	}
}

// Replay 返回 seq 严格大于 afterSeq 的事件。
// 需要的序号已被淘汰时返回 ok=false，调用方必须要求客户端重新同步。
func (r *Ring) Replay(afterSeq uint64) (items []Item, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) == 0 {
		return nil, true
	}
	oldest := r.items[0].Seq
	if afterSeq > 0 && afterSeq+1 < oldest {
		return nil, false
	}
	for _, it := range r.items {
		if it.Seq > afterSeq {
			items = append(items, it)
		}
	}
	return items, true
}

// Stats 返回缓冲规模，用于诊断。
func (r *Ring) Stats() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return map[string]any{
		"items":   len(r.items),
		"bytes":   r.bytes,
		"max":     r.maxItems,
		"maxByte": r.maxBytes,
		"dropped": r.dropped,
	}
}

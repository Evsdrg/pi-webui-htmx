package sessions

import (
	"encoding/json"
	"testing"
)

// Pi 把 usage 写在 assistant 消息上，形状见真实 JSONL。
// 这里必须用真实形状：字段名拼错会让解析静默返回 nil，
// 于是前端永远看不到用量，而测试若用自造形状就抓不到。
func Test解析助手用量(t *testing.T) {
	raw := json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"hi"}],
	  "usage":{"input":9639,"output":151,"cacheRead":128,"cacheWrite":0,"reasoning":33,
	  "totalTokens":9918,"cost":{"input":0.019278,"output":0.000906,"cacheRead":6.4e-05,"cacheWrite":0,"total":0.020244}},
	  "stopReason":"toolUse"}`)
	got := parseUsage(raw)
	if got == nil {
		t.Fatal("应解析出 usage")
	}
	if got.Input != 9639 || got.Output != 151 || got.CacheRead != 128 || got.CacheWrite != 0 {
		t.Fatalf("token 字段不符：%+v", got)
	}
	if got.Cost < 0.0202 || got.Cost > 0.0203 {
		t.Fatalf("费用应取 cost.total：%v", got.Cost)
	}
}

// 没有 usage 的记录必须返回 nil 而不是全零值，否则展示层无法区分
// 「没有记录」与「记录为零」，会把空摘要渲染出来。
func Test没有用量时返回nil(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[]}`),
		json.RawMessage(`{"role":"user","content":[]}`),
		json.RawMessage(`{}`),
		nil,
	} {
		if got := parseUsage(raw); got != nil {
			t.Fatalf("%s 应返回 nil，得到 %+v", raw, got)
		}
	}
}

func Test用量摘要格式(t *testing.T) {
	cases := []struct {
		usage Usage
		want  string
	}{
		{Usage{Input: 138, Output: 754, CacheRead: 237952, Cost: 0.0079}, "138 in · 754 out · 237952 cache R · $0.0079"},
		// 零值整段省略，不是显示成 0
		{Usage{Input: 100, Output: 0, CacheRead: 0, Cost: 0}, "100 in"},
		{Usage{CacheRead: 5000, CacheWrite: 200, Cost: 0.5}, "5000 cache R · 200 cache W · $0.5000"},
		{Usage{}, ""},
	}
	for _, tc := range cases {
		if got := tc.usage.Summary(); got != tc.want {
			t.Fatalf("摘要 %q，期望 %q", got, tc.want)
		}
	}
}

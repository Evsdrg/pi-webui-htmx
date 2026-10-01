package sessions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func Test用户图片和助手失败进入安全历史投影(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"type":"message","id":"u1","message":{"role":"user","content":[{"type":"text","text":"颜色？"},{"type":"image","mimeType":"image/png","data":"aGVsbG8="}]}}`),
		json.RawMessage(`{"type":"message","id":"a1","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"402 {\"error\":{\"message\":\"insufficient balance (1008)\"},\"request_id\":\"private-request-id\"}"}}`),
	}
	entries := ProjectEntries(rows)
	if len(entries) != 2 || len(entries[0].Lazy) != 1 || entries[0].Lazy[0] != (LazyBlock{Kind: "image", BlockIndex: 1}) {
		t.Fatalf("用户附带图片未生成可定位占位符: %+v", entries)
	}
	if !strings.Contains(entries[1].Error, "余额") || strings.Contains(entries[1].Error, "private-request-id") || strings.Contains(entries[1].Error, "request_id") {
		t.Fatalf("供应商失败应有安全摘要且不泄漏原始内容: %+v", entries[1])
	}
}

func TestUserImage读取与角色隔离(t *testing.T) {
	image := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n-demo"))
	store, id := writeLazySession(t, []map[string]any{
		{"type": "message", "id": "u1", "parentId": nil, "message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "text", "text": "图在哪？"}, {"type": "image", "mimeType": "image/png", "data": image},
		}}},
		{"type": "message", "id": "t1", "parentId": "u1", "message": map[string]any{"role": "toolResult", "content": []map[string]any{
			{"type": "image", "mimeType": "image/png", "data": image},
		}}},
		{"type": "message", "id": "u2", "parentId": "t1", "message": map[string]any{"role": "user", "content": []map[string]any{
			{"type": "image", "mimeType": "image/svg+xml", "data": image},
		}}},
	})
	body, mime, err := store.UserImage(context.Background(), id, "u1", 1)
	if err != nil || mime != "image/png" || string(body) != "\x89PNG\r\n\x1a\n-demo" {
		t.Fatalf("用户图片读取失败: mime=%s err=%v", mime, err)
	}
	if _, _, err := store.ToolImage(context.Background(), id, "u1", 1); err == nil {
		t.Error("工具图片入口不应读取用户图片")
	}
	if _, _, err := store.UserImage(context.Background(), id, "t1", 0); err == nil {
		t.Error("用户图片入口不应读取工具结果")
	}
	if _, _, err := store.UserImage(context.Background(), id, "u2", 0); err == nil {
		t.Error("用户图片不能绕过 SVG 格式限制")
	}
}

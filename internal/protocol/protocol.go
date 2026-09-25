// Package protocol 定义桥与浏览器之间的 v1 消息封装与错误码。
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Version 是当前桥对外协议版本，升级必须同步更新 api/v1/protocol.md。
const Version = 1

// Request 是浏览器发来的命令封装。
type Request struct {
	Version   int             `json:"version"`
	Kind      string          `json:"kind"`
	RequestID string          `json:"requestId"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// Error 是协议错误，Code 为稳定错误码，Message 面向使用者展示。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// E 构造一个带中文提示的协议错误。
func E(code, message string) error { return &Error{Code: code, Message: message} }

// Message 是桥向外发送的响应、事件或控制消息。
type Message struct {
	Version   int    `json:"version"`
	Kind      string `json:"kind"`
	RequestID string `json:"requestId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	OK        *bool  `json:"ok,omitempty"`
	Data      any    `json:"data,omitempty"`
	Error     *Error `json:"error,omitempty"`
	StreamID  string `json:"streamId,omitempty"`
	Epoch     string `json:"epoch,omitempty"`
	Seq       uint64 `json:"seq,omitempty"`
	Event     string `json:"event,omitempty"`
}

// Reply 生成一条响应；失败时不把 data 一起返回，避免误读为部分成功。
func Reply(id string, data any, err error) Message {
	ok := err == nil
	m := Message{Version: Version, Kind: "response", RequestID: id, OK: &ok, Data: data}
	if err != nil {
		m.Data = nil
		var pe *Error
		if errors.As(err, &pe) {
			m.Error = pe
		} else {
			m.Error = &Error{Code: "internal", Message: "操作失败"}
		}
	}
	return m
}

// Decode 解析参数对象：禁止未知字段、拒绝 null，且只接受一个 JSON 值。
func Decode(raw []byte, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return E("invalid_params", "参数必须是一个对象")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return E("invalid_params", "参数存在无效字段或未知字段")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return E("invalid_params", "一次只能发送一个 JSON 值")
	}
	return nil
}

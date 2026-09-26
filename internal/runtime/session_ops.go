package runtime

import (
	"context"
	"encoding/json"

	"pi-bridge-go/internal/protocol"
)

// Models 返回当前配置可用的模型列表。
// 只投影前端需要的字段，不透传供应商私有配置。
func (w *Worker) Models(ctx context.Context) ([]map[string]any, error) {
	raw, err := w.call(ctx, "get_available_models", nil, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Models []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			Provider      string   `json:"provider"`
			API           string   `json:"api"`
			Reasoning     bool     `json:"reasoning"`
			Input         []string `json:"input"`
			ContextWindow int      `json:"contextWindow"`
			MaxTokens     int      `json:"maxTokens"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的模型列表无效")
	}
	list := make([]map[string]any, 0, len(out.Models))
	for _, m := range out.Models {
		list = append(list, map[string]any{
			"id": m.ID, "name": m.Name, "provider": m.Provider, "api": m.API,
			"reasoning": m.Reasoning, "input": m.Input,
			"contextWindow": m.ContextWindow, "maxTokens": m.MaxTokens,
		})
	}
	return list, nil
}

// SetModel 切换模型，返回切换后的模型标识。
func (w *Worker) SetModel(ctx context.Context, provider, modelID string) (map[string]any, error) {
	if provider == "" || modelID == "" {
		return nil, protocol.E("invalid_params", "provider 与 modelId 均不能为空")
	}
	raw, err := w.call(ctx, "set_model", map[string]any{"provider": provider, "modelId": modelID}, true)
	if err != nil {
		return nil, err
	}
	var m struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
	}
	if json.Unmarshal(raw, &m) != nil || m.ID == "" {
		return nil, protocol.E("pi_error", "Pi 返回的模型无效")
	}
	return map[string]any{"id": m.ID, "name": m.Name, "provider": m.Provider}, nil
}

// CycleModel 切换到下一个可用模型。
func (w *Worker) CycleModel(ctx context.Context) (map[string]any, error) {
	raw, err := w.call(ctx, "cycle_model", nil, true)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" || len(raw) == 0 {
		return nil, nil
	}
	var out struct {
		Model struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
		ThinkingLevel string `json:"thinkingLevel"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的模型无效")
	}
	return map[string]any{"id": out.Model.ID, "provider": out.Model.Provider, "thinkingLevel": out.ThinkingLevel}, nil
}

// ThinkingLevels 返回当前模型支持的思考强度。
func (w *Worker) ThinkingLevels(ctx context.Context) ([]string, error) {
	raw, err := w.call(ctx, "get_available_thinking_levels", nil, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Levels []string `json:"levels"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的思考等级无效")
	}
	return out.Levels, nil
}

// SetThinkingLevel 设置思考强度； level 必须出现在可用列表内。
func (w *Worker) SetThinkingLevel(ctx context.Context, level string) error {
	if level == "" {
		return protocol.E("invalid_params", "level 不能为空")
	}
	levels, err := w.ThinkingLevels(ctx)
	if err != nil {
		return err
	}
	if !containsString(levels, level) {
		return protocol.E("invalid_params", "当前模型不支持该思考等级")
	}
	_, err = w.call(ctx, "set_thinking_level", map[string]any{"level": level}, true)
	return err
}

// CycleThinkingLevel 切换到下一个思考强度。
func (w *Worker) CycleThinkingLevel(ctx context.Context) (string, error) {
	raw, err := w.call(ctx, "cycle_thinking_level", nil, true)
	if err != nil {
		return "", err
	}
	var out struct {
		Level string `json:"level"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", protocol.E("pi_error", "Pi 返回的思考等级无效")
	}
	return out.Level, nil
}

// SetQueueMode 设置 steering 或 followUp 的投递模式。
func (w *Worker) SetQueueMode(ctx context.Context, kind, mode string) error {
	if mode != "all" && mode != "one-at-a-time" {
		return protocol.E("invalid_params", "mode 只能是 all 或 one-at-a-time")
	}
	var method string
	switch kind {
	case "steering":
		method = "set_steering_mode"
	case "followUp":
		method = "set_follow_up_mode"
	default:
		return protocol.E("invalid_params", "kind 只能是 steering 或 followUp")
	}
	_, err := w.call(ctx, method, map[string]any{"mode": mode}, true)
	return err
}

// Steer 在当前回合结束后插入一条引导消息。
func (w *Worker) Steer(ctx context.Context, text string, images []map[string]any) error {
	if text == "" {
		return protocol.E("invalid_params", "消息不能为空")
	}
	_, err := w.call(ctx, "steer", messageFields(text, images), true)
	return err
}

// FollowUp 排队一条后续消息，等 agent 完全结束后再投递。
func (w *Worker) FollowUp(ctx context.Context, text string, images []map[string]any) error {
	if text == "" {
		return protocol.E("invalid_params", "消息不能为空")
	}
	_, err := w.call(ctx, "follow_up", messageFields(text, images), true)
	return err
}

// Compact 手动压缩上下文。
func (w *Worker) Compact(ctx context.Context, instructions string) (map[string]any, error) {
	fields := map[string]any{}
	if instructions != "" {
		fields["customInstructions"] = instructions
	}
	raw, err := w.call(ctx, "compact", fields, true)
	if err != nil {
		return nil, err
	}
	var out struct {
		Summary              string `json:"summary"`
		FirstKeptEntryID     string `json:"firstKeptEntryId"`
		TokensBefore         int    `json:"tokensBefore"`
		EstimatedTokensAfter int    `json:"estimatedTokensAfter"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的压缩结果无效")
	}
	return map[string]any{
		"summary": out.Summary, "firstKeptEntryId": out.FirstKeptEntryID,
		"tokensBefore": out.TokensBefore, "estimatedTokensAfter": out.EstimatedTokensAfter,
	}, nil
}

// SetAutoCompaction 开关自动压缩。
func (w *Worker) SetAutoCompaction(ctx context.Context, enabled bool) error {
	_, err := w.call(ctx, "set_auto_compaction", map[string]any{"enabled": enabled}, true)
	return err
}

// SetAutoRetry 开关自动重试。
func (w *Worker) SetAutoRetry(ctx context.Context, enabled bool) error {
	_, err := w.call(ctx, "set_auto_retry", map[string]any{"enabled": enabled}, true)
	return err
}

// AbortRetry 中止正在进行的重试等待。
func (w *Worker) AbortRetry(ctx context.Context) error {
	_, err := w.call(ctx, "abort_retry", nil, true)
	return err
}

// Stats 返回会话用量统计。
func (w *Worker) Stats(ctx context.Context) (map[string]any, error) {
	raw, err := w.call(ctx, "get_session_stats", nil, false)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的统计无效")
	}
	return out, nil
}

// SetName 设置会话显示名。
func (w *Worker) SetName(ctx context.Context, name string) error {
	if name == "" {
		return protocol.E("invalid_params", "会话名不能为空")
	}
	if len(name) > 200 {
		return protocol.E("invalid_params", "会话名过长")
	}
	_, err := w.call(ctx, "set_session_name", map[string]any{"name": name}, true)
	return err
}

// LastAssistantText 取最后一条助手文本。
func (w *Worker) LastAssistantText(ctx context.Context) (string, error) {
	raw, err := w.call(ctx, "get_last_assistant_text", nil, false)
	if err != nil {
		return "", err
	}
	var out struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", protocol.E("pi_error", "Pi 返回的文本无效")
	}
	if out.Text == nil {
		return "", nil
	}
	return *out.Text, nil
}

// Commands 返回可用的扩展命令、提示模板与技能。
func (w *Worker) Commands(ctx context.Context) ([]map[string]any, error) {
	raw, err := w.call(ctx, "get_commands", nil, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Commands []map[string]any `json:"commands"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的命令列表无效")
	}
	return out.Commands, nil
}

// Tree 返回会话树与当前叶子。
func (w *Worker) Tree(ctx context.Context) (map[string]any, error) {
	raw, err := w.call(ctx, "get_tree", nil, false)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的会话树无效")
	}
	return out, nil
}

// ForkMessages 返回可供 fork 的用户消息。
func (w *Worker) ForkMessages(ctx context.Context) ([]map[string]any, error) {
	raw, err := w.call(ctx, "get_fork_messages", nil, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Messages []map[string]any `json:"messages"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的 fork 列表无效")
	}
	return out.Messages, nil
}

// Entries 返回追加顺序的条目；since 为空时返回全部。
// 注意：这是追加游标，包含其他分支与压缩前历史，不能当分支分页用。
func (w *Worker) Entries(ctx context.Context, since string, limit int) (map[string]any, error) {
	if limit <= 0 || limit > 500 {
		return nil, protocol.E("invalid_params", "limit 必须在 1 到 500 之间")
	}
	fields := map[string]any{}
	if since != "" {
		fields["since"] = since
	}
	raw, err := w.call(ctx, "get_entries", fields, false)
	if err != nil {
		return nil, err
	}
	var out struct {
		Entries []json.RawMessage `json:"entries"`
		LeafID  string            `json:"leafId"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return nil, protocol.E("pi_error", "Pi 返回的条目无效")
	}
	if len(out.Entries) > limit {
		out.Entries = out.Entries[:limit]
	}
	return map[string]any{"entries": out.Entries, "leafId": out.LeafID}, nil
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ExportHTML 让 Pi 把当前会话导出为 HTML。
// 输出路径由桥构造并校验，不接受调用方任意指定。
func (w *Worker) ExportHTML(ctx context.Context, outputPath string) (string, error) {
	if outputPath == "" {
		return "", protocol.E("invalid_params", "outputPath 不能为空")
	}
	raw, err := w.call(ctx, "export_html", map[string]any{"outputPath": outputPath}, false)
	if err != nil {
		return "", err
	}
	var out struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Path == "" {
		return "", protocol.E("pi_error", "Pi 返回的导出路径无效")
	}
	return out.Path, nil
}

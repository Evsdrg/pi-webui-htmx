package presentation

// RenderThinking 仅排版纯文本，模板自动转义；旧JSON读取协议仍保留。
func (r *Renderer) RenderThinking(text string) (string, error) {
	return r.execute("thinking.html", text)
}

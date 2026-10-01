package presentation

import (
	"strings"

	"pi-bridge-go/internal/management"
)

// RenderDiscoveredModels 排版供应商返回的数据，不把列表渲染推给浏览器。
func (r *Renderer) RenderDiscoveredModels(rows []management.DiscoveredModel) (string, error) {
	return r.execute("model-discovery.html", rows)
}

// CatalogRow 是模型目录片段的一行。字段与 management.DiscoveredModel 同形，
// 但模板要读的是「显示用」的名字，所以单独定一层。
type CatalogRow struct {
	ID          string
	Name        string
	ContextSize int
	MaxTokens   int
	Reasoning   bool
	Image       bool
}

// RenderCatalog 渲染模型目录候选。
// 目录有上万条，按需搜索后一次最多渲染 50 条——
// 把整份目录塞进页面既不必要，也会让面板体积失控。
func (r *Renderer) RenderCatalog(items []management.DiscoveredModel, total int, query string) (string, error) {
	rows := make([]CatalogRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, CatalogRow{
			ID: item.ID, Name: item.Name, ContextSize: item.ContextSize,
			MaxTokens: item.MaxTokens, Reasoning: item.Reasoning, Image: hasImageInput(item.Input),
		})
	}
	title := ""
	if strings.TrimSpace(query) != "" {
		title = query
	}
	return r.execute("catalog.html", struct {
		Rows  []CatalogRow
		Total int
		Query string
	}{rows, total, title})
}

// hasImageInput 判断目录里的 input 字段是否声明了图片能力。
func hasImageInput(input []string) bool {
	for _, v := range input {
		if v == "image" {
			return true
		}
	}
	return false
}

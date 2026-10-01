package presentation

import (
	"sort"
	"strconv"
	"strings"
)

// ConfigModels 把 models.json 的 providers 子树投影成可展示的模型清单，
// 只保留展示字段。模型列表的两种形状（数组、以 id 为键的对象）都接受，
// 但注意上层的 management.Config.Models 只放行数组——对象分支是给
// 直接调用者的容错，不是线上会出现的情形。
//
// 参数就是 providers 本身（`{"provider": {...}, ...}`），不是整份文档。
// 曾经它的参数是整份文档（内部取 doc["providers"]），K2 批次把回执
// 改成具名类型时调用点改传了 `reply.Providers`，函数内部却还在找
// `doc["providers"]`——于是永远取到 nil，模型选择器一直是空的，
// 而空列表看上去就像“用户没配模型”，不报错、不显眼。
// 改成直接收 providers 子树，让调用点与参数含义一致。
func ConfigModels(providers map[string]any) []map[string]any {
	out := []map[string]any{}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, provider := range names {
		entry, _ := providers[provider].(map[string]any)
		add := func(id string, value any) {
			if id == "" || len(out) >= 512 {
				return
			}
			model, _ := value.(map[string]any)
			name := stringField(model, "name")
			if name == "" {
				name = id
			}
			out = append(out, map[string]any{"id": id, "name": name, "provider": provider})
		}
		switch models := entry["models"].(type) {
		case []any:
			for _, value := range models {
				model, _ := value.(map[string]any)
				add(stringField(model, "id"), model)
			}
		case map[string]any:
			ids := make([]string, 0, len(models))
			for id := range models {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				add(id, models[id])
			}
		}
	}
	return out
}

// ParseDiff 只解析用于展示的行类型和行号，文本仍由 html/template 转义。
func ParseDiff(raw string) []DiffFile {
	files := []DiffFile{}
	current := -1
	oldNo, newNo := 0, 0
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			files = append(files, DiffFile{Path: strings.TrimPrefix(line, "diff --git ")})
			current = len(files) - 1
			oldNo, newNo = 0, 0
			continue
		}
		if current < 0 {
			continue
		}
		file := &files[current]
		switch {
		case strings.HasPrefix(line, "new file mode"):
			file.IsNew = true
		case strings.HasPrefix(line, "deleted file mode"):
			file.IsDeleted = true
		case strings.HasPrefix(line, "Binary files"):
			file.IsBinary = true
		case strings.HasPrefix(line, "+++ b/"):
			file.Path = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "@@"):
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				oldNo, _ = strconv.Atoi(strings.Split(strings.TrimPrefix(fields[1], "-"), ",")[0])
				newNo, _ = strconv.Atoi(strings.Split(strings.TrimPrefix(fields[2], "+"), ",")[0])
			}
			file.Lines = append(file.Lines, DiffLine{Kind: "hunk", Text: line})
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "index "):
		case strings.HasPrefix(line, "+"):
			file.Lines = append(file.Lines, DiffLine{Kind: "add", NewNo: newNo, Text: line[1:]})
			newNo++
		case strings.HasPrefix(line, "-"):
			file.Lines = append(file.Lines, DiffLine{Kind: "del", OldNo: oldNo, Text: line[1:]})
			oldNo++
		case strings.HasPrefix(line, " "):
			file.Lines = append(file.Lines, DiffLine{Kind: "context", OldNo: oldNo, NewNo: newNo, Text: line[1:]})
			oldNo++
			newNo++
		}
	}
	return files
}

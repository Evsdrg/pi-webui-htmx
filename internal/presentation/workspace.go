package presentation

import (
	"sort"
	"strconv"
	"strings"
)

// ConfigModels 兼容 Pi 的模型数组与旧桥的对象配置，只投影展示字段。
func ConfigModels(doc map[string]any) []map[string]any {
	out := []map[string]any{}
	providers, _ := doc["providers"].(map[string]any)
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

package management

import "testing"

func Test模型数值限制拒绝小数和非正数(t *testing.T) {
	c := NewConfig(t.TempDir(), DefaultLimits())
	for _, v := range []any{0.0, -1.0, 1.5, "4096"} {
		if err := c.validateModelsDocument(map[string]any{"providers": map[string]any{"p": map[string]any{"models": []any{map[string]any{"id": "m", "contextWindow": v}}}}}); err == nil {
			t.Errorf("未拒绝 %v", v)
		}
	}
}

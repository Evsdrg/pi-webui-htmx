package management

import "encoding/json"

// jsonUnmarshalForTest 供测试直接复用解析逻辑。
func jsonUnmarshalForTest(b []byte, v any) error { return json.Unmarshal(b, v) }

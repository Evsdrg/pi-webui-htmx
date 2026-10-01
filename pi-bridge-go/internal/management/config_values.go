package management

import (
	"strings"

	"pi-bridge-go/internal/protocol"
)

const secretPlaceholder = "***"

// 字段名只用于字段对象；provider/modelOverrides 的键是身份，不能按字段脱敏。
var secretKeys = map[string]bool{
	"apikey": true, "api_key": true, "token": true, "secret": true,
	"password": true, "authorization": true, "auth": true,
	"credential": true, "credentials": true,
}

type configScope uint8

const (
	configObject configScope = iota
	providerNames
	providerFields
	modelList
	modelNames
	modelFields
	headerValues
)

func childScope(scope configScope, key string) configScope {
	switch scope {
	case providerNames:
		return providerFields
	case modelNames:
		return modelFields
	case providerFields:
		if key == "models" {
			return modelList
		}
		if key == "modelOverrides" {
			return modelNames
		}
	}
	if scope == configObject && key == "providers" {
		return providerNames
	}
	if key == "headers" {
		return headerValues
	}
	return configObject
}

// mapConfig 是读取脱敏与保存保留的共同遍历器，返回副本，不改变调用方对象。
// 已知模型数组按 ID 匹配；头部按不区分大小写的名称匹配。
func mapConfig(value, previous any, scope configScope, redact bool, depth int) (any, error) {
	if depth > 128 {
		return nil, protocol.E("limit_exceeded", "配置嵌套过深")
	}
	if scope == headerValues {
		if _, ok := value.(map[string]any); !ok {
			if redact {
				return secretPlaceholder, nil
			}
			return nil, protocol.E("invalid_params", "headers 必须是对象")
		}
	}
	switch v := value.(type) {
	case map[string]any:
		old, _ := previous.(map[string]any)
		if scope == headerValues && !redact {
			canonical := make(map[string]any, len(old))
			for name, val := range old {
				lower := strings.ToLower(name)
				if _, exists := canonical[lower]; exists {
					// 歧义项不能用于保留；明确的新值仍可修复原配置。
					canonical[lower] = nil
				} else {
					canonical[lower] = val
				}
			}
			old = canonical
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			prior := old[key]
			if scope == headerValues && !redact {
				prior = old[strings.ToLower(key)]
			}
			secret := scope == headerValues || (scope != providerNames && scope != modelNames && secretKeys[strings.ToLower(key)])
			if secret {
				if redact {
					out[key] = secretPlaceholder
					continue
				}
				if item == secretPlaceholder {
					if prior == nil || prior == secretPlaceholder {
						return nil, protocol.E("invalid_params", "秘密占位符无法匹配原值，请明确填写或移除该字段")
					}
					item = prior
				}
				// 只检查 Pi 实际解析为命令的字段；普通名称/未知文本中的 ! 不是命令。
				if text, ok := item.(string); ok && (scope == headerValues || key == "apiKey") && strings.HasPrefix(text, "!") {
					if original, ok := prior.(string); !ok || original != text {
						return nil, protocol.E("invalid_params", "网页不能新增或修改命令型凭据，只能保留本机已有表达式")
					}
				}
				out[key] = item
				continue
			}
			mapped, err := mapConfig(item, prior, childScope(scope, key), redact, depth+1)
			if err != nil {
				return nil, err
			}
			out[key] = mapped
		}
		return out, nil
	case []any:
		old, _ := previous.([]any)
		byID := make(map[string]any)
		if scope == modelList && !redact {
			for _, item := range old {
				model, _ := item.(map[string]any)
				id, _ := model["id"].(string)
				if id == "" {
					continue
				}
				if _, exists := byID[id]; exists {
					byID[id] = nil
				} else {
					byID[id] = item
				}
			}
		}
		out := make([]any, len(v))
		for i, item := range v {
			var prior any
			child := configObject
			if scope == modelList {
				model, _ := item.(map[string]any)
				id, _ := model["id"].(string)
				prior, child = byID[id], modelFields
			} else if i < len(old) {
				prior = old[i]
			}
			mapped, err := mapConfig(item, prior, child, redact, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = mapped
		}
		return out, nil
	default:
		return value, nil
	}
}

func redactObject(doc map[string]any) (map[string]any, error) {
	value, err := mapConfig(doc, nil, configObject, true, 0)
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}

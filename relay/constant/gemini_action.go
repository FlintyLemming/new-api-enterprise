package constant

import "strings"

// GeminiAction 表示一个 Gemini relay 请求最终执行的动作分类。
type GeminiAction int

const (
	GeminiActionUnknown GeminiAction = iota
	GeminiActionGenerate
	GeminiActionEmbedding
	GeminiActionPredict
)

// ClassifyGeminiAction 按设计文档规则判定一个 Gemini relay 请求的最终动作分类,
// 判定顺序固定为「最终 model 前缀优先于 path action」。
// path 是入站请求 path(允许带 query);modelName 是模型映射完成后的最终上游模型名。
// 本函数是纯函数:不读全局配置、不产生副作用,空入参返回 GeminiActionUnknown。
func ClassifyGeminiAction(path string, modelName string) GeminiAction {
	if strings.HasPrefix(modelName, "imagen") {
		return GeminiActionPredict
	}
	for _, prefix := range []string{"text-embedding", "embedding", "gemini-embedding"} {
		if strings.HasPrefix(modelName, prefix) {
			return GeminiActionEmbedding
		}
	}

	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	segment := path[strings.LastIndexByte(path, '/')+1:]
	colon := strings.LastIndexByte(segment, ':')
	if colon < 0 {
		// 没有显式 :action 的 path(例如 /v1/engines/{model}/embeddings)不做后缀特判。
		return GeminiActionUnknown
	}
	switch segment[colon+1:] {
	case "generateContent", "streamGenerateContent":
		return GeminiActionGenerate
	case "embedContent", "batchEmbedContents":
		return GeminiActionEmbedding
	case "predict":
		return GeminiActionPredict
	default:
		return GeminiActionUnknown
	}
}

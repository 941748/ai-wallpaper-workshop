// Package assets 内嵌客户端资产(编译进 exe, 无运行时依赖)。
package assets

import _ "embed"

// WorkflowTemplate ComfyUI API 格式工作流模板(含占位符)。
//
//go:embed workflow_template.json
var WorkflowTemplate []byte

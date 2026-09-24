package provider

import "github.com/cloudwego/eino/components/tool"

type Groups struct {
	Direct   []tool.BaseTool
	Analysis []tool.BaseTool
	QA       []tool.BaseTool
	Report   []tool.BaseTool
}

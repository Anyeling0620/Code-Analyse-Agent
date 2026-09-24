package provider

import (
	"context"
	"edu.agent.code/config"
	"edu.agent.code/service/tool/http_request"
	"edu.agent.code/service/tool/project_scan"
	"edu.agent.code/service/tool/project_search"
	"edu.agent.code/service/tool/read_files"
	"edu.agent.code/service/tool/terminal"
	"github.com/cloudwego/eino/components/tool"
)

type LocalLoader struct {
	conf *config.Config
	// TODO 后续可加入MySQL
}

func NewLocalLoader(conf *config.Config) *LocalLoader {
	return &LocalLoader{conf}
}

func (l *LocalLoader) Load(ctx context.Context) (Groups, error) {
	analysisTools, err := newAnalysisTools()
	if err != nil {
		return Groups{}, err
	}
	qaTools, err := newQATools()
	if err != nil {
		return Groups{}, err
	}
	directTools, err := newDirectTools()
	if err != nil {
		return Groups{}, err
	}
	return Groups{
		Direct:   directTools,
		Analysis: analysisTools,
		QA:       qaTools,
		Report:   nil,
	}, nil
}

func (l *LocalLoader) Close() error {
	return nil
}

func newAnalysisTools() ([]tool.BaseTool, error) {
	projectScanTool, err := project_scan.NewTool()
	if err != nil {
		return nil, err
	}
	projectSearchTool, err := project_search.NewTool()
	if err != nil {
		return nil, err
	}
	readFilesTool, err := read_files.NewTool()
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{projectScanTool, projectSearchTool, readFilesTool}, nil
}

func newQATools() ([]tool.BaseTool, error) {
	projectScanTool, err := project_scan.NewTool()
	if err != nil {
		return nil, err
	}
	projectSearchTool, err := project_search.NewTool()
	if err != nil {
		return nil, err
	}
	readFilesTool, err := read_files.NewTool()
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{projectScanTool, projectSearchTool, readFilesTool}, nil
}

func newDirectTools() ([]tool.BaseTool, error) {
	terminalTool, err := terminal.NewTool()
	if err != nil {
		return nil, err
	}
	httpRequestTool, err := http_request.NewTool()
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{terminalTool, httpRequestTool}, nil
}

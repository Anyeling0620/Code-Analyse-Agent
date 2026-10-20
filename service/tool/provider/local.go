package provider

import (
	"context"
	"edu.agent.code/config"
	"edu.agent.code/service/tool/http_request"
	"edu.agent.code/service/tool/project_scan"
	"edu.agent.code/service/tool/project_search"
	"edu.agent.code/service/tool/read_files"
	"edu.agent.code/service/tool/self_report"
	"edu.agent.code/service/tool/terminal"
	"edu.agent.code/utils/logger"
	"errors"
	"github.com/cloudwego/eino/components/tool"
)

type LocalLoader struct {
	conf       *config.Config
	selfReport *self_report.Reader
}

func NewLocalLoader(conf *config.Config) *LocalLoader {
	return &LocalLoader{conf: conf}
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

	selfTools, selfReporter, err := newSelfReportTools(l.conf)
	if err != nil {
		if selfReporter != nil {
			closeErr := selfReporter.Close()
			if closeErr != nil {
				logger.Error("close self report failed", closeErr)
			}
		}
		return Groups{}, err
	}
	l.selfReport = selfReporter

	return Groups{
		Direct:     directTools,
		Analysis:   analysisTools,
		QA:         qaTools,
		SelfReport: selfTools,
	}, nil
}

func (l *LocalLoader) Close() error {
	var err error
	if l.selfReport != nil {
		if closeErr := l.selfReport.Close(); closeErr != nil {
			logger.Error("close self report loader failed", closeErr)
			err = errors.Join(err, closeErr)
		}
	}
	return err
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

// newSelfReportTools 构造自省报表工具（连 agent_telemetry 的独立只读账号）。
//
// 工具集指向 agent_telemetry 库，访问控制由白名单门禁 + 独立只读账号共同保证。
// 不启用（enable=false 或 dsn 为空）时返回空工具集而不是报错——
// 自省能力是可选增强，缺配置不该让服务起不来。
func newSelfReportTools(conf *config.Config) ([]tool.BaseTool, *self_report.Reader, error) {
	if conf == nil || !conf.SelfReport.Enable || conf.SelfReport.DSN == "" {
		return []tool.BaseTool{}, nil, nil
	}
	reporter, err := self_report.NewReader(conf.SelfReport)
	if err != nil {
		return nil, nil, err
	}
	tools, err := self_report.NewTools(reporter, conf.SelfReport.AllowedUsers)
	if err != nil {
		closeErr := reporter.Close()
		if closeErr != nil {
			logger.Error("close self report reporter failed", closeErr)
		}
		return nil, nil, errors.Join(err, closeErr)
	}
	if len(conf.SelfReport.AllowedUsers) == 0 {
		// 白名单为空 = 谁都不放行，工具装了也没人能调用。这通常说明配置漏了，
		// 明确告警一次，避免上线后以为自省报表"坏了"。
		logger.Warn("self_report 已启用但 allowed_users 为空：自省报表对所有人不可用")
	}
	return tools, reporter, nil
}

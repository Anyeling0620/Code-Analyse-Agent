package provider

import (
	"context"
	"edu.agent.code/config"
	"edu.agent.code/service/tool/db_report"
	"edu.agent.code/service/tool/http_request"
	"edu.agent.code/service/tool/project_scan"
	"edu.agent.code/service/tool/project_search"
	"edu.agent.code/service/tool/read_files"
	"edu.agent.code/service/tool/terminal"
	"edu.agent.code/utils/logger"
	"errors"
	"github.com/cloudwego/eino/components/tool"
)

type LocalLoader struct {
	conf       *config.Config
	dbReporter *db_report.DBReport
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

	dbReportTools, dbReporter, err := newReportTools(l.conf)
	if err != nil {
		if dbReporter != nil {
			closeErr := dbReporter.Close()
			if closeErr != nil {
				logger.Error("close LocalLoader failed", closeErr)
			}
		}
		return Groups{}, err
	}
	l.dbReporter = dbReporter

	return Groups{
		Direct:   directTools,
		Analysis: analysisTools,
		QA:       qaTools,
		Report:   dbReportTools,
	}, nil
}

func (l *LocalLoader) Close() error {
	if l.dbReporter != nil {
		closeErr := l.dbReporter.Close()
		if closeErr != nil {
			logger.Error("close loader failed", closeErr)
			return closeErr
		}
	}
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

func newReportTools(conf *config.Config) ([]tool.BaseTool, *db_report.DBReport, error) {
	if conf == nil || conf.DatabaseReport.Enable == false {
		return []tool.BaseTool{}, nil, nil
	}
	reporter, err := db_report.NewDBReport(conf.DatabaseReport)
	if err != nil {
		return nil, nil, err
	}
	tools, err := db_report.NewTools(reporter)
	if err != nil {
		closeErr := reporter.Close()
		if closeErr != nil {
			logger.Error("close reporter failed", closeErr)
		}
		return nil, nil, errors.Join(err, closeErr)
	}
	return tools, reporter, nil
}

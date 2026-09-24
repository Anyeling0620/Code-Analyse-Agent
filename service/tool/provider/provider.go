package provider

import (
	"context"
	"edu.agent.code/adaptor"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
)

type Groups struct {
	Direct   []tool.BaseTool
	Analysis []tool.BaseTool
	QA       []tool.BaseTool
	Report   []tool.BaseTool
}

type IProvider interface {
	ILoader
	RetrieverTool(ctx context.Context, collection string) (tool.BaseTool, error)
}

type Provider struct {
	adaptor adaptor.IAdaptor
	loaders []ILoader
}

func NewProvider(adaptor adaptor.IAdaptor) *Provider {
	conf := adaptor.GetConfig()

	loaders := []ILoader{
		NewLocalLoader(conf),
	}
	if conf != nil && conf.MCP.Enable {
		loaders = append(loaders, NewMCPLoader(conf.MCP))
	}
	return &Provider{
		loaders: loaders,
		adaptor: adaptor,
	}
}

func (p *Provider) Load(ctx context.Context) (Groups, error) {
	var groups Groups
	for _, loader := range p.loaders {
		loaded, err := loader.Load(ctx)
		if err != nil {
			return Groups{}, err
		}
		groups.Direct = append(groups.Direct, loaded.Direct...)
		groups.Analysis = append(groups.Analysis, loaded.Analysis...)
		groups.Report = append(groups.Report, loaded.Report...)
		groups.QA = append(groups.QA, loaded.QA...)
	}
	return groups, nil
}

func (p *Provider) Close() error {
	var err error
	for _, loader := range p.loaders {
		closeErr := loader.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if err != nil {
		return fmt.Errorf("error closing provider: %w", err)
	}
	return nil
}

func (p *Provider) RetrieverTool(ctx context.Context, collection string) (tool.BaseTool, error) {
	return nil, nil

}

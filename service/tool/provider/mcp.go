package provider

import (
	"context"
	"edu.agent.code/config"
	"errors"
	"fmt"
	mcpcomponent "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/client"
	mcpc "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"strings"
	"time"
)

const (
	mcpDefaultTimeoutSecond = 30
)

type MCPLoader struct {
	conf    config.MCP
	clients []client.MCPClient
}

func NewMCPLoader(conf config.MCP) *MCPLoader {
	return &MCPLoader{
		conf: conf,
	}
}

func (m *MCPLoader) Load(ctx context.Context) (Groups, error) {
	var groups Groups
	for _, server := range m.conf.Servers {
		// 版本变更 server.Enabled 从 *bool 变成 bool
		if !server.Enabled {
			continue
		}
		load, cli, err := m.loadServer(ctx, server)
		if err != nil {
			if server.Required {
				return Groups{}, err
			}
		}
		m.clients = append(m.clients, cli)
		appendMCPTools(&groups, server.Groups, load)
	}
	return groups, nil
}

func appendMCPTools(groups *Groups, targetGroup []string, tools []tool.BaseTool) {
	if len(targetGroup) == 0 {
		targetGroup = []string{"analysis"}
	}
	for _, group := range targetGroup {
		switch group {
		case "direct":
			groups.Direct = append(groups.Direct, tools...)
		case "", "analysis":
			groups.Analysis = append(groups.Analysis, tools...)
		case "qa":
			groups.QA = append(groups.QA, tools...)
		case "report":
			groups.Report = append(groups.Report, tools...)
		}

	}
}

func (m *MCPLoader) Close() error {
	var err error
	for _, cli := range m.clients {
		cliErr := cli.Close()
		if cliErr != nil {
			err = errors.Join(err, cliErr)
		}
	}
	m.clients = nil
	if err != nil {
		return fmt.Errorf("close mcp clients: %w", err)
	}
	return nil
}

func (m *MCPLoader) loadServer(ctx context.Context, server config.MCPServer) (
	[]tool.BaseTool, client.MCPClient, error) {
	serverName := server.Name
	if serverName == "" {
		return nil, nil, errors.New("server name is empty")
	}
	timeoutSec := server.InitTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = mcpDefaultTimeoutSecond
	}
	serverCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cli, err := newMCPClient(server)
	if err != nil {
		return nil, nil, fmt.Errorf("new client: %w, serverName: %s", err, serverName)
	}
	started := false
	defer func() {
		// 没启动但是有client 直接关闭
		if !started && cli != nil {
			_ = cli.Close()
		}
	}()

	err = startMCPClient(serverCtx, server.Transport, cli)
	if err != nil {
		return nil, nil, fmt.Errorf("startMCPClient: %w, serverName: %s", err, serverName)
	}
	initRequest := initMCPRequest()
	// TODO 这里用ctx还是serverCtx 有异议
	_, err = cli.Initialize(serverCtx, initRequest)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize: %w", err)
	}
	tools, err := mcpcomponent.GetTools(ctx, &mcpcomponent.Config{
		Cli:           cli,
		ToolNameList:  server.IncludeTools,
		CustomHeaders: server.Headers,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("get tools: %w", err)
	}
	// 如果获取的工具与已有工具名字冲突，加前缀
	toolNamePrefix := mcpToolPrefix(serverName, server.ToolPrefix)
	tools, err = prefixMCPTools(toolNamePrefix, tools)
	if err != nil {
		return nil, nil, fmt.Errorf("prefixMCPTools: %w", err)
	}
	started = true
	return tools, cli, nil
}

func prefixMCPTools(prefix string, tools []tool.BaseTool) ([]tool.BaseTool, error) {
	prefix = strings.TrimSpace(prefix)
	if len(prefix) == 0 {
		return tools, nil
	}
	prefixed := make([]tool.BaseTool, 0, len(tools))
	for _, item := range tools {
		if item == nil {
			continue
		}
		info, err := item.Info(context.Background())
		if err != nil {
			return nil, err
		}
		if info == nil || info.Name == "" {
			return nil, fmt.Errorf("info %s is empty", item)
		}
		prefixed = append(prefixed, &mcpTool{
			BaseTool: item,
			name:     prefix + "-" + info.Name,
		})
	}
	return prefixed, nil
}

type mcpTool struct {
	tool.BaseTool
	name string
}

func (m *mcpTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	info, err := m.BaseTool.Info(ctx)
	if err != nil {
		return nil, err
	}
	copied := *info
	copied.Name = m.name
	return &copied, nil

}

func (m *mcpTool) InvokableRun(ctx context.Context, argumentsInJson string, opts ...tool.Option) (string, error) {
	invokable, ok := m.BaseTool.(tool.InvokableTool)
	if !ok {
		return "", errors.New("not invokable")
	}
	return invokable.InvokableRun(ctx, argumentsInJson, opts...)
}

func mcpToolPrefix(serverName string, toolPrefix string) string {
	if strings.TrimSpace(toolPrefix) != "" {
		return toolPrefix
	}
	return serverName
}

func initMCPRequest() mcp.InitializeRequest {
	conf := config.GetLatestConfig()
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{
		Name:    config.ServerFullName,
		Version: conf.Server.Version,
		Title:   conf.Server.AppName,
	}
	return request
}

func startMCPClient(ctx context.Context, transport string, cli client.MCPClient) error {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport != "sse" && transport != "streamable" {
		return nil
	}

	type starter interface {
		Start(context.Context) error
	}
	started, ok := cli.(starter)
	if !ok {
		return nil
	}
	return started.Start(ctx)
}

func newMCPClient(server config.MCPServer) (client.MCPClient, error) {
	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	switch transport {
	case "", "stdio":
		if server.Command == "" {
			return nil, errors.New("server command is empty")
		}
		return client.NewStdioMCPClient(server.Command, mcpEnv(server.Env), server.Args...)
	case "sse":
		if server.URL == "" {
			return nil, errors.New("server URL is empty")
		}
		return client.NewSSEMCPClient(server.URL, client.WithHeaders(server.Headers))
	case "streamable":
		if server.URL == "" {
			return nil, errors.New("server URL is empty")
		}

		return client.NewStreamableHttpClient(server.URL, mcpc.WithHTTPHeaders(server.Headers))
	default:
		return nil, errors.New("server transport type is invalid")
	}
}

func mcpEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	items := make([]string, 0, len(env))
	for k, v := range env {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		items = append(items, fmt.Sprintf("%s=%s", key, v))
	}
	return items
}

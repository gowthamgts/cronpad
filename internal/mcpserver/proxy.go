package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct{ key string }

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+t.key)
	return http.DefaultTransport.RoundTrip(copy)
}

// HTTPClient supplies a bearer credential and refuses redirects to avoid leaking it.
func HTTPClient(key string) *http.Client {
	return &http.Client{Transport: bearerTransport{key}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Endpoint validates a client URL and appends an endpoint path.
func Endpoint(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("server URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("server URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	return strings.TrimRight(base, "/") + path, nil
}

// Proxy serves stdio while forwarding tools, resources and prompts over HTTP.
// Both transports are owned by the caller context and closed on EOF/cancellation.
func Proxy(ctx context.Context, endpoint, key string, transport mcp.Transport) error {
	remote, err := mcp.NewClient(&mcp.Implementation{Name: "cronpad-stdio", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: HTTPClient(key), DisableStandaloneSSE: true}, nil)
	if err != nil {
		return fmt.Errorf("connect MCP: %w", err)
	}
	defer func() { _ = remote.Close() }()
	local := mcp.NewServer(&mcp.Implementation{Name: "cronpad", Version: "1"}, nil)
	tools, err := remote.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	for _, tool := range tools.Tools {
		local.AddTool(tool, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return remote.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: r.Params.Arguments})
		})
	}
	handler := func(ctx context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return remote.ReadResource(ctx, r.Params)
	}
	resources, err := remote.ListResources(ctx, nil)
	if err != nil {
		return fmt.Errorf("list resources: %w", err)
	}
	for _, resource := range resources.Resources {
		local.AddResource(resource, handler)
	}
	templates, err := remote.ListResourceTemplates(ctx, nil)
	if err != nil {
		return fmt.Errorf("list resource templates: %w", err)
	}
	for _, template := range templates.ResourceTemplates {
		local.AddResourceTemplate(template, handler)
	}
	prompts, err := remote.ListPrompts(ctx, nil)
	if err != nil {
		return fmt.Errorf("list prompts: %w", err)
	}
	for _, prompt := range prompts.Prompts {
		local.AddPrompt(prompt, func(ctx context.Context, r *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return remote.GetPrompt(ctx, r.Params)
		})
	}
	return local.Run(ctx, transport)
}

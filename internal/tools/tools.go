// Package tools defines the built-in tool interface and registry.
// Tools are trusted Go functions that agents can call via LLM tool-use.
// These are NOT skills — they're part of the controller runtime.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is a built-in capability the agent can invoke via LLM function calling.
type Tool interface {
	// Name returns the tool's function name (e.g., "search", "bash", "read_file").
	Name() string

	// Description returns a short description for the LLM.
	Description() string

	// Parameters returns the JSON Schema for this tool's parameters.
	Parameters() json.RawMessage

	// Run executes the tool with the given JSON arguments and returns a string result.
	Run(ctx context.Context, args json.RawMessage) (string, error)

	// NeedsApproval returns true if this tool requires user confirmation before running.
	NeedsApproval() bool
}

// Registry holds all available tools.
type Registry struct {
	tools map[string]Tool
	order []string // preserve registration order
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
	r.order = append(r.order, t.Name())
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All returns all registered tools in registration order.
func (r *Registry) All() []Tool {
	var result []Tool
	for _, name := range r.order {
		result = append(result, r.tools[name])
	}
	return result
}

// OpenAITools returns the tool definitions in OpenAI API format.
func (r *Registry) OpenAITools() []OpenAITool {
	var tools []OpenAITool
	for _, t := range r.All() {
		tools = append(tools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return tools
}

// OpenAITool is the tool definition format for the OpenAI API.
type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction is the function definition within an OpenAI tool.
type OpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ResponsesTool is the tool definition format for the OpenAI Responses API.
// Unlike OpenAITool, the function fields are flat (not nested under a "function" key).
type ResponsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ResponsesTools returns the tool definitions in OpenAI Responses API format.
func (r *Registry) ResponsesTools() []ResponsesTool {
	var result []ResponsesTool
	for _, t := range r.All() {
		result = append(result, ResponsesTool{
			Type:        "function",
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return result
}

// ApprovalFunc is called when a tool needs user approval before running.
// It receives the tool name and a human-readable description of what will happen.
// Returns true if approved.
type ApprovalFunc func(toolName string, description string) bool

// Execute runs a tool by name with the given args. If the tool needs approval,
// it calls the approval function first.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage, approve ApprovalFunc) (string, error) {
	t, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	if t.NeedsApproval() && approve != nil {
		desc := fmt.Sprintf("Tool %q wants to run with args: %s", name, string(args))
		if !approve(name, desc) {
			return "Tool execution denied by user.", nil
		}
	}

	return t.Run(ctx, args)
}

// Package channels implements channel adapters for agent communication.
// The CLI channel is the first — interactive terminal chat.
package channels

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"smithly.dev/internal/agent"
)

// CLI runs an interactive terminal chat with an agent.
type CLI struct {
	Agent  *agent.Agent
	Input  io.Reader
	Output io.Writer
}

// Run starts the interactive chat loop. It blocks until the user types "exit" or EOF.
func (c *CLI) Run(ctx context.Context) error {
	if c.Input == nil {
		c.Input = os.Stdin
	}
	if c.Output == nil {
		c.Output = os.Stdout
	}

	scanner := bufio.NewScanner(c.Input)
	name := c.Agent.Workspace.Identity.Name
	if name == "" {
		name = c.Agent.ID
	}

	toolCount := len(c.Agent.Tools.All())
	fmt.Fprintf(c.Output, "Smithly — chatting with %s (%d tools available, type 'exit' to quit)\n\n", name, toolCount)

	for {
		fmt.Fprint(c.Output, "you> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			fmt.Fprintln(c.Output, "Goodbye.")
			return nil
		}

		fmt.Fprintf(c.Output, "\n%s> ", name)

		cb := &agent.Callbacks{
			OnDelta: func(token string) {
				fmt.Fprint(c.Output, token)
			},
			OnToolCall: func(toolName string, args string) {
				fmt.Fprintf(c.Output, "\n  [tool: %s]\n", toolName)
			},
			OnToolResult: func(toolName string, result string) {
				// Show a summary of the result
				summary := result
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				// Indent the result
				lines := strings.Split(summary, "\n")
				for _, line := range lines {
					fmt.Fprintf(c.Output, "  | %s\n", line)
				}
				fmt.Fprintln(c.Output)
			},
			Approve: func(toolName string, description string) bool {
				fmt.Fprintf(c.Output, "\n  [%s requires approval]\n  %s\n  Allow? [y/N]: ", toolName, description)
				if !scanner.Scan() {
					return false
				}
				answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
				return answer == "y" || answer == "yes"
			},
			OnPaused: func(used int, limit int) {
				fmt.Fprintf(c.Output, "\n  [paused] Token limit reached: %d / %d tokens used.\n", used, limit)
				fmt.Fprintf(c.Output, "  Agent will not respond until the limit is raised or reset.\n\n")
			},
		}

		_, err := c.Agent.Chat(ctx, input, cb)
		if err != nil {
			fmt.Fprintf(c.Output, "\nerror: %v\n\n", err)
			continue
		}
		fmt.Fprint(c.Output, "\n\n")
	}

	return scanner.Err()
}

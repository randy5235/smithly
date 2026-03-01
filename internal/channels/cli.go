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
	"sync"
	"time"

	"golang.org/x/term"

	"smithly.dev/internal/agent"
)

// CLI runs an interactive terminal chat with an agent.
type CLI struct {
	Agent  *agent.Agent
	Input  io.Reader
	Output io.Writer
}

// Run starts the interactive chat loop. It blocks until the user types "exit" or EOF.
// When stdin is a real terminal, raw mode is enabled so ESC can interrupt a running query.
func (c *CLI) Run(ctx context.Context) error {
	if c.Input == nil {
		c.Input = os.Stdin
	}
	if c.Output == nil {
		c.Output = os.Stdout
	}

	// Use raw mode when stdin is a real terminal (enables ESC interrupt).
	if f, ok := c.Input.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		return c.runRaw(ctx, f)
	}
	return c.runScanner(ctx)
}

// runScanner is the original scanner-based loop, used for non-terminal input (pipes, tests).
func (c *CLI) runScanner(ctx context.Context) error {
	scanner := bufio.NewScanner(c.Input)
	name := c.Agent.Workspace.Identity.Name
	if name == "" {
		name = c.Agent.ID
	}

	toolCount := len(c.Agent.Tools.All())
	fmt.Fprintf(c.Output, "Smithly — chatting with %s (%d tools available, type 'exit' to quit)\n", name, toolCount)
	if len(c.Agent.CostWindows) > 0 {
		fmt.Fprintf(c.Output, "  Cost limits active. Note: cost estimates are approximate and may not\n")
		fmt.Fprintf(c.Output, "  match your provider's billing. Monitor your provider dashboard.\n")
	}
	fmt.Fprintln(c.Output)

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
				summary := result
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
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
			OnPaused: func(window string, remaining time.Duration) {
				fmt.Fprintf(c.Output, "\n  [paused] %s spending limit reached. Resets in %s.\n\n", window, remaining.Round(time.Minute))
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

// runRaw runs the chat loop in raw terminal mode, enabling ESC-to-interrupt.
func (c *CLI) runRaw(ctx context.Context, stdin *os.File) error {
	fd := int(stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return c.runScanner(ctx)
	}
	defer term.Restore(fd, oldState)

	name := c.Agent.Workspace.Identity.Name
	if name == "" {
		name = c.Agent.ID
	}
	toolCount := len(c.Agent.Tools.All())
	out := c.Output

	// rprint writes to output, converting \n to \r\n for raw mode.
	rprint := func(s string) {
		fmt.Fprint(out, strings.ReplaceAll(s, "\n", "\r\n"))
	}

	rprint(fmt.Sprintf("Smithly — chatting with %s (%d tools, 'exit' to quit, ESC to type ahead)\r\n", name, toolCount))
	if len(c.Agent.CostWindows) > 0 {
		rprint("  Cost limits active. Note: cost estimates are approximate and may not\r\n")
		rprint("  match your provider's billing. Monitor your provider dashboard.\r\n")
	}
	rprint("\r\n")

	// One goroutine owns stdin reads; everything reads from bytesCh.
	bytesCh := make(chan byte, 64)
	go func() {
		defer close(bytesCh)
		b := make([]byte, 1)
		for {
			n, err := stdin.Read(b)
			if err != nil || n == 0 {
				return
			}
			bytesCh <- b[0]
		}
	}()

	var typeAhead string
	for {
		var line string
		if typeAhead != "" {
			line = typeAhead
			typeAhead = ""
		} else {
			rprint("you> ")
			var ok bool
			line, ok = readRawLine(bytesCh, out)
			if !ok {
				rprint("\r\n")
				break
			}
		}
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			rprint("Goodbye.\r\n")
			return nil
		}

		rprint(fmt.Sprintf("\r\n%s> ", name))

		queryCtx, cancelQuery := context.WithCancel(ctx)
		var interrupted bool
		typeAhead, interrupted = c.runWithInterrupt(queryCtx, cancelQuery, bytesCh, line, name, rprint)
		cancelQuery()

		if interrupted {
			rprint("\r\n  [interrupted]\r\n")
		}
		rprint("\r\n")
	}

	return nil
}

// readRawLine reads one line from bytesCh in raw mode, echoing printable characters.
// Returns ("", false) on EOF, Ctrl-D, or Ctrl-C.
func readRawLine(bytesCh <-chan byte, out io.Writer) (string, bool) {
	var buf []byte
	for b := range bytesCh {
		switch b {
		case '\r', '\n':
			return string(buf), true
		case 0x7f, '\b': // backspace / delete
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				fmt.Fprint(out, "\b \b")
			}
		case 0x03: // Ctrl+C
			fmt.Fprint(out, "^C\r\n")
			return "", false
		case 0x04: // Ctrl+D (EOF)
			return "", false
		case 0x1b: // ESC during input — clear the current line
			for len(buf) > 0 {
				fmt.Fprint(out, "\b \b")
				buf = buf[:len(buf)-1]
			}
		default:
			if b >= 0x20 && b < 0x7f { // printable ASCII
				buf = append(buf, b)
				fmt.Fprintf(out, "%c", b)
			}
		}
	}
	return "", false // channel closed (stdin EOF)
}

// runWithInterrupt runs the agent and watches bytesCh for ESC (type-ahead)
// and Ctrl+C (cancel). On ESC the agent keeps running while the user types
// their next message. Returns the type-ahead text and whether interrupted.
func (c *CLI) runWithInterrupt(
	ctx context.Context,
	cancel context.CancelFunc,
	bytesCh <-chan byte,
	input string,
	name string,
	rprint func(string),
) (string, bool) {
	// mu guards writes to output so the type-ahead prompt never interleaves
	// with streaming tokens; paused suppresses callbacks after ESC.
	var mu sync.Mutex
	paused := false

	out := c.Output
	rprintLocked := func(s string) {
		fmt.Fprint(out, strings.ReplaceAll(s, "\n", "\r\n"))
	}

	cb := &agent.Callbacks{
		OnDelta: func(token string) {
			mu.Lock()
			defer mu.Unlock()
			if !paused {
				rprintLocked(token)
			}
		},
		OnToolCall: func(toolName string, args string) {
			mu.Lock()
			defer mu.Unlock()
			if !paused {
				rprintLocked(fmt.Sprintf("\r\n  [tool: %s]\r\n", toolName))
			}
		},
		OnToolResult: func(toolName string, result string) {
			mu.Lock()
			defer mu.Unlock()
			if !paused {
				summary := result
				if len(summary) > 200 {
					summary = summary[:200] + "..."
				}
				for _, l := range strings.Split(summary, "\n") {
					rprintLocked(fmt.Sprintf("  | %s\r\n", l))
				}
				rprintLocked("\r\n")
			}
		},
		Approve: func(toolName string, description string) bool {
			mu.Lock()
			if paused {
				mu.Unlock()
				return false // auto-deny: user can't see the prompt
			}
			rprintLocked(fmt.Sprintf("\r\n  [%s requires approval]\r\n  %s\r\n  Allow? [y/N]: ", toolName, description))
			mu.Unlock()
			select {
			case b, ok := <-bytesCh:
				if !ok {
					return false
				}
				mu.Lock()
				rprintLocked(fmt.Sprintf("%c\r\n", b))
				mu.Unlock()
				return b == 'y' || b == 'Y'
			case <-ctx.Done():
				return false
			}
		},
		OnPaused: func(window string, remaining time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			if !paused {
				rprintLocked(fmt.Sprintf("\r\n  [paused] %s spending limit reached. Resets in %s.\r\n\r\n", window, remaining.Round(time.Minute)))
			}
		},
	}

	type chatResult struct{ err error }
	resultCh := make(chan chatResult, 1)
	go func() {
		_, err := c.Agent.Chat(ctx, input, cb)
		resultCh <- chatResult{err}
	}()

	for {
		select {
		case r := <-resultCh:
			if r.err != nil && r.err != context.Canceled {
				rprint(fmt.Sprintf("\r\nerror: %v\r\n", r.err))
			}
			return "", false

		case b, ok := <-bytesCh:
			if !ok {
				// stdin closed — cancel and drain
				cancel()
				<-resultCh
				return "", false
			}

			// Ctrl+C: immediate cancel
			if b == 0x03 {
				rprint("^C\r\n")
				cancel()
				<-resultCh
				return "", true
			}

			if b != 0x1b {
				continue // ignore other input while agent runs
			}

			// ESC: pause streaming output, enter type-ahead mode.
			// The agent continues running in the background.
			mu.Lock()
			paused = true
			mu.Unlock()
			rprint("\r\n  (Ctrl+C to cancel)\r\nyou> ")

			// Type-ahead loop: collect input while agent finishes.
			// Use nil-channel trick: once resultCh fires, stop selecting it.
			var buf []byte
			activeResultCh := (<-chan chatResult)(resultCh)
			for {
				select {
				case <-activeResultCh:
					activeResultCh = nil // agent done, keep collecting input

				case tb, ok := <-bytesCh:
					if !ok {
						if activeResultCh != nil {
							cancel()
							<-resultCh
						}
						return "", false
					}
					switch tb {
					case '\r', '\n':
						rprint("\r\n")
						if activeResultCh != nil {
							<-resultCh // wait for agent to finish
						}
						return string(buf), false
					case 0x7f, '\b':
						if len(buf) > 0 {
							buf = buf[:len(buf)-1]
							rprint("\b \b")
						}
					case 0x03:
						rprint("^C\r\n")
						if activeResultCh != nil {
							cancel()
							<-resultCh
						}
						return "", true
					case 0x1b: // ESC again — clear the line
						for len(buf) > 0 {
							rprint("\b \b")
							buf = buf[:len(buf)-1]
						}
					default:
						if tb >= 0x20 && tb < 0x7f {
							buf = append(buf, tb)
							rprint(fmt.Sprintf("%c", tb))
						}
					}
				}
			}
		}
	}
}

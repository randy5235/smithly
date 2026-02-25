package agent

import "fmt"

// loopDetector watches for repetitive tool call patterns in the agent loop.
// If the same tool+args combination is seen too many times in a row,
// it signals a loop.
type loopDetector struct {
	history   []string // recent tool call signatures
	threshold int      // how many repeats before triggering
}

func newLoopDetector() *loopDetector {
	return &loopDetector{threshold: 3}
}

// record adds a tool call to the history and returns true if a loop is detected.
func (ld *loopDetector) record(toolName, args string) bool {
	sig := fmt.Sprintf("%s:%s", toolName, args)
	ld.history = append(ld.history, sig)

	// Check if the last N entries are all the same
	n := len(ld.history)
	if n < ld.threshold {
		return false
	}

	last := ld.history[n-1]
	for i := n - ld.threshold; i < n; i++ {
		if ld.history[i] != last {
			return false
		}
	}
	return true
}

// recordResponse checks for repeated identical text responses.
func (ld *loopDetector) recordResponse(content string) bool {
	if content == "" {
		return false
	}
	sig := "response:" + content
	ld.history = append(ld.history, sig)

	n := len(ld.history)
	if n < ld.threshold {
		return false
	}

	last := ld.history[n-1]
	for i := n - ld.threshold; i < n; i++ {
		if ld.history[i] != last {
			return false
		}
	}
	return true
}

package agent

import "testing"

func TestLoopDetectorNoLoop(t *testing.T) {
	ld := newLoopDetector()
	if ld.record("search", `{"q":"a"}`) {
		t.Error("should not detect loop on first call")
	}
	if ld.record("fetch", `{"url":"b"}`) {
		t.Error("different tools should not trigger loop")
	}
	if ld.record("search", `{"q":"c"}`) {
		t.Error("different args should not trigger loop")
	}
}

func TestLoopDetectorRepeatedToolCalls(t *testing.T) {
	ld := newLoopDetector()

	ld.record("search", `{"q":"weather"}`)
	ld.record("search", `{"q":"weather"}`)

	if !ld.record("search", `{"q":"weather"}`) {
		t.Error("3 identical calls should trigger loop detection")
	}
}

func TestLoopDetectorMixedCalls(t *testing.T) {
	ld := newLoopDetector()

	ld.record("search", `{"q":"a"}`)
	ld.record("search", `{"q":"a"}`)
	ld.record("fetch", `{"url":"x"}`) // breaks the streak
	ld.record("search", `{"q":"a"}`)

	if ld.record("search", `{"q":"a"}`) {
		t.Error("streak was broken, should not detect loop")
	}
}

func TestLoopDetectorRepeatedResponses(t *testing.T) {
	ld := newLoopDetector()

	ld.recordResponse("I don't know")
	ld.recordResponse("I don't know")

	if !ld.recordResponse("I don't know") {
		t.Error("3 identical responses should trigger loop detection")
	}
}

func TestLoopDetectorEmptyResponse(t *testing.T) {
	ld := newLoopDetector()

	if ld.recordResponse("") {
		t.Error("empty responses should not trigger loop")
	}
}

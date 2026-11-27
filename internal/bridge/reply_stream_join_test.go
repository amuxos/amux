package bridge

import (
	"testing"
	"time"
)

func newJoinTestState() *replyStreamState {
	backend := &orderedReplyBackend{
		create:         func(string) (*replyCardHandle, error) { return &replyCardHandle{}, nil },
		update:         func(*replyCardHandle, string) error { return nil },
		finish:         func(*replyCardHandle, string) error { return nil },
		createActivity: func(turnCardActivity) (*replyCardHandle, error) { return &replyCardHandle{}, nil },
		updateActivity: func(*replyCardHandle, turnCardActivity) error { return nil },
	}
	return &replyStreamState{sender: &streamingReplySender{minInterval: time.Hour, backend: backend}}
}

func TestReplySegmentsJoinWithParagraphBreak(t *testing.T) {
	t.Parallel()
	s := newJoinTestState()
	s.full.WriteString("第一段：\n\n```text\na b c\n```")
	// A separate assistant message arriving with no current message.
	s.replaceCurrent("```text\nz y x\n```\n\n完成。")
	want := "第一段：\n\n```text\na b c\n```\n\n```text\nz y x\n```\n\n完成。"
	if got := s.full.String(); got != want {
		t.Fatalf("full = %q\nwant %q", got, want)
	}
}

func TestReplyReplaceKeepsParagraphBreakBeforeCurrent(t *testing.T) {
	t.Parallel()
	s := newJoinTestState()
	s.full.WriteString("```text\na\n```")
	s.current.WriteString("")
	s.replaceCurrent("草稿")
	s.replaceCurrent("```text\nb\n```")
	if got := s.full.String(); got != "```text\na\n```\n\n```text\nb\n```" {
		t.Fatalf("full = %q", got)
	}
}

func TestReplyDedupesIdenticalConsecutiveSegment(t *testing.T) {
	t.Parallel()
	s := newJoinTestState()
	s.full.WriteString("开始处理。\n\n1+32=33")
	s.replaceCurrent("1+32=33")
	if got := s.full.String(); got != "开始处理。\n\n1+32=33" {
		t.Fatalf("duplicate segment kept: %q", got)
	}
	s2 := newJoinTestState()
	s2.full.WriteString("1+32=33")
	s2.reconcileFinalMessage("1+32=33")
	if got := s2.full.String(); got != "1+32=33" {
		t.Fatalf("final duplicate kept: %q", got)
	}
}

func TestReplyContinuationStaysOnSameLine(t *testing.T) {
	t.Parallel()
	s := newJoinTestState()
	s.replaceCurrent("Hello")
	s.replaceCurrent("Hello world")
	if got := s.full.String(); got != "Hello world" {
		t.Fatalf("full = %q", got)
	}
}

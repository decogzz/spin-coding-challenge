package requestid_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"gitlab.com/decogzz/spin-coding-challenge/internal/requestid"
)

func TestLogHandlerAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(requestid.LogHandler{Handler: slog.NewTextHandler(&buf, nil)}).With("component", "test")

	logger.InfoContext(requestid.NewContext(context.Background(), "req-42"), "with id")
	logger.InfoContext(context.Background(), "without id")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "request_id=req-42") || !strings.Contains(lines[0], "component=test") {
		t.Errorf("first line missing attributes: %s", lines[0])
	}
	if strings.Contains(lines[1], "request_id") {
		t.Errorf("second line must not have a request_id: %s", lines[1])
	}
}

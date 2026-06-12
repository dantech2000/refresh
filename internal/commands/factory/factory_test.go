package factory_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/dantech2000/refresh/internal/commands/factory"
)

func TestNewDefaultLogger_NonNilPassthrough(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if factory.NewDefaultLogger(logger) != logger {
		t.Fatal("NewDefaultLogger should return the provided logger unchanged")
	}
}

func TestNewDefaultLogger_NilReturnsLogger(t *testing.T) {
	if factory.NewDefaultLogger(nil) == nil {
		t.Fatal("NewDefaultLogger(nil) must not return nil")
	}
}

// REF-37: --log-level strings map to slog levels; unknown falls back to warn.
func TestParseLogLevel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelWarn},
		{"DEBUG", slog.LevelDebug},
		{"bogus", slog.LevelWarn},
	} {
		if got := factory.ParseLogLevel(tc.in); got != tc.want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// REF-37: SetDefaultLogLevel changes the level NewDefaultLogger emits at.
func TestSetDefaultLogLevel(t *testing.T) {
	t.Cleanup(func() { factory.SetDefaultLogLevel(slog.LevelWarn) })
	factory.SetDefaultLogLevel(slog.LevelDebug)
	l := factory.NewDefaultLogger(nil)
	if !l.Enabled(nil, slog.LevelDebug) {
		t.Error("after SetDefaultLogLevel(debug), logger should emit at debug")
	}
	factory.SetDefaultLogLevel(slog.LevelError)
	l = factory.NewDefaultLogger(nil)
	if l.Enabled(nil, slog.LevelWarn) {
		t.Error("after SetDefaultLogLevel(error), logger should NOT emit at warn")
	}
}

func TestNewAddonService(t *testing.T) {
	if factory.NewAddonService(aws.Config{Region: "us-east-1"}, nil) == nil {
		t.Fatal("NewAddonService returned nil")
	}
}

func TestNewClusterService_WithoutHealth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if factory.NewClusterService(aws.Config{Region: "us-east-1"}, false, logger) == nil {
		t.Fatal("NewClusterService without health returned nil")
	}
}

func TestNewClusterService_WithHealth(t *testing.T) {
	if factory.NewClusterService(aws.Config{Region: "us-east-1"}, true, nil) == nil {
		t.Fatal("NewClusterService with health returned nil")
	}
}

func TestNewNodegroupService_WithoutHealth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if factory.NewNodegroupService(aws.Config{Region: "us-east-1"}, false, logger) == nil {
		t.Fatal("NewNodegroupService without health returned nil")
	}
}

func TestNewNodegroupService_WithHealth(t *testing.T) {
	if factory.NewNodegroupService(aws.Config{Region: "us-east-1"}, true, nil) == nil {
		t.Fatal("NewNodegroupService with health returned nil")
	}
}

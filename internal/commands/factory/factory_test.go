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

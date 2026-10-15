package awsx

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/smithy-go/middleware"
)

// At the debug level each attempt of a call is one line, so a retried
// call shows its retries. Above it, nothing is logged.
//
// The default logger is one for the process, so the test does not run in
// parallel, and it puts the logger back.
func TestLogAttempts(t *testing.T) {
	// A listener that is closed again leaves a port that refuses.
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		level     slog.Level
		wantLines int
	}{
		{level: slog.LevelDebug, wantLines: maxAttempts},
		{level: slog.LevelWarn, wantLines: 0},
	} {
		t.Run(tt.level.String(), func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: tt.level})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			client := NewHTTPClient(8)
			defer client.GetTransport().CloseIdleConnections()
			cfg := aws.Config{
				Region:       "us-east-1",
				BaseEndpoint: aws.String(endpoint),
				HTTPClient:   client,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "AKIDFAKEFAKEFAKE", SecretAccessKey: "fake"}, nil
				}),
				APIOptions: []func(*middleware.Stack) error{logAttempts},
			}
			provider := NewProvider(cfg, func(o *ProviderOptions) { o.Backoff = zeroBackoff{} })

			if _, err := provider.clientsFor("", Standard).EKS.ListClusters(context.Background(), &eks.ListClustersInput{}); err == nil {
				t.Fatal("the call to a closed port succeeded")
			}

			got := strings.Count(buf.String(), `msg="AWS call attempt failed" service=EKS operation=ListClusters region=us-east-1`)
			if got != tt.wantLines {
				t.Errorf("lines = %d, want %d:\n%s", got, tt.wantLines, buf.String())
			}
			if tt.wantLines > 0 && !strings.Contains(buf.String(), "retryable=true") {
				t.Errorf("a refused connection is not logged as retryable:\n%s", buf.String())
			}
		})
	}
}

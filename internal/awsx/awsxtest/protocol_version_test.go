package awsxtest_test

import (
	"net/http"
	"testing"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The server answers both protocols: HTTP/1.1 for Config and for a program
// that only has the URL, and HTTP/2 for MultiplexedConfig.
func TestServerSpeaksBothProtocols(t *testing.T) {
	srv := awsxtest.NewServer(fleettest.New())
	defer srv.Close()
	tests := []struct {
		name   string
		client doer
		want   int
	}{
		{"Config", srv.Config("us-east-1").HTTPClient, 1},
		{"MultiplexedConfig", srv.MultiplexedConfig("us-east-1").HTTPClient, 2},
		{"a plain client", http.DefaultClient, 1},
	}
	for _, tt := range tests {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/clusters", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := tt.client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		_ = resp.Body.Close()
		if resp.ProtoMajor != tt.want {
			t.Errorf("%s: the answer came over HTTP/%d, want HTTP/%d", tt.name, resp.ProtoMajor, tt.want)
		}
	}
}

// doer is the HTTP client of an SDK config.
type doer interface {
	Do(*http.Request) (*http.Response, error)
}

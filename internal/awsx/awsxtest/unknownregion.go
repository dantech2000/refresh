package awsxtest

import (
	"net"
	"net/http"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// WithUnknownRegions returns cfg with an HTTP client that fails every
// request for one of regions as the DNS lookup of its AWS endpoint fails
// for a region that does not exist: "dial tcp: lookup
// eks.us-east-9.amazonaws.com: no such host". Requests for any other region
// reach the server as before.
func WithUnknownRegions(cfg aws.Config, regions ...string) aws.Config {
	next := cfg.HTTPClient
	cfg.HTTPClient = &http.Client{Transport: unknownRegions{next: next, regions: regions}}
	return cfg
}

type unknownRegions struct {
	next    aws.HTTPClient
	regions []string
}

func (u unknownRegions) RoundTrip(req *http.Request) (*http.Response, error) {
	m := credentialScope.FindStringSubmatch(req.Header.Get("Authorization"))
	if len(m) == 3 && slices.Contains(u.regions, m[1]) {
		host := m[2] + "." + m[1] + ".amazonaws.com"
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{
			Err: "no such host", Name: host, IsNotFound: true,
		}}
	}
	return u.next.Do(req)
}

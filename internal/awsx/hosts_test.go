package awsx

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A region with a typing error gives an endpoint name that does not exist.
// The message is the region help for every endpoint family, not the generic
// network advice.
func TestMalformedRegionOnEveryEndpointFamily(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "standard", host: "eks.us-esat-1.amazonaws.com", want: true},
		{name: "dual-stack", host: "eks.us-esat-1.api.aws", want: true},
		{name: "FIPS", host: "eks-fips.us-esat-1.amazonaws.com", want: true},
		{name: "GovCloud", host: "eks.us-gov-esat-1.amazonaws.com", want: true},
		{name: "China", host: "eks.cn-nroth-1.amazonaws.com.cn", want: true},
		{name: "China dual-stack", host: "eks.cn-nroth-1.api.amazonwebservices.com.cn", want: true},
		{name: "European Sovereign Cloud", host: "eks.eusc-de-esat-1.amazonaws.eu", want: true},
		{name: "European Sovereign Cloud dual-stack", host: "eks.eusc-de-esat-1.api.amazonwebservices.eu", want: true},
		{name: "ISO", host: "eks.us-iso-esat-1.c2s.ic.gov", want: true},
		{name: "ISO dual-stack", host: "eks.us-iso-esat-1.api.aws.ic.gov", want: true},
		{name: "ISO-B", host: "eks.us-isob-esat-1.sc2s.sgov.gov", want: true},
		{name: "ISO-B dual-stack", host: "eks.us-isob-esat-1.api.aws.scloud", want: true},
		{name: "ISO-E", host: "eks.eu-isoe-wset-1.cloud.adc-e.uk", want: true},
		{name: "ISO-F", host: "eks.us-isof-suoth-1.csp.hci.ic.gov", want: true},
		{name: "trailing dot and capitals", host: "EKS.us-esat-1.API.AWS.", want: true},
		{name: "a proxy", host: "proxy.corp.example", want: false},
		{name: "a name that only ends like one", host: "notamazonaws.com", want: false},
		{name: "another aws name", host: "console.aws", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dns := &net.DNSError{Err: "no such host", Name: tt.host, IsNotFound: true}
			err := sdkOpErr(&url.Error{Op: "Get", URL: "https://" + tt.host + "/clusters", Err: &net.OpError{Op: "dial", Net: "tcp", Err: dns}})

			if got := IsRegionError(err); got != tt.want {
				t.Errorf("IsRegionError() = %t, want %t", got, tt.want)
			}
			msg := FormatAWSError(err, "listing clusters").Error()
			wantPrefix := "network connectivity issue while listing clusters"
			if tt.want {
				wantPrefix = "AWS region configuration issue while listing clusters"
			}
			if !strings.HasPrefix(msg, wantPrefix) {
				t.Errorf("message starts with %q, want %q", strings.SplitN(msg, "\n", 2)[0], wantPrefix)
			}
		})
	}
}

// The suffix list is the one of the SDK version in go.mod. When the SDK
// gets a partition, this test names the suffix to add.
func TestAWSDNSSuffixesMatchTheSDK(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-m", "-f", "{{.Dir}}", "github.com/aws/aws-sdk-go-v2").Output()
	if err != nil {
		t.Skipf("cannot find the SDK module: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "internal", "endpoints", "awsrulesfn", "partitions.json"))
	if err != nil {
		t.Skipf("cannot read the partition data of the SDK: %v", err)
	}
	var partitions struct {
		Partitions []struct {
			ID      string `json:"id"`
			Outputs struct {
				DNSSuffix          string `json:"dnsSuffix"`
				DualStackDNSSuffix string `json:"dualStackDnsSuffix"`
			} `json:"outputs"`
		} `json:"partitions"`
	}
	if err := json.Unmarshal(data, &partitions); err != nil {
		t.Fatalf("reading the partition data: %v", err)
	}
	if len(partitions.Partitions) == 0 {
		t.Fatal("the partition data has no partitions; its format changed")
	}
	var want []string
	for _, p := range partitions.Partitions {
		for _, suffix := range []string{p.Outputs.DNSSuffix, p.Outputs.DualStackDNSSuffix} {
			if suffix == "" {
				continue
			}
			want = append(want, "."+suffix)
			if !slices.Contains(awsDNSSuffixes, "."+suffix) {
				t.Errorf("partition %s: awsDNSSuffixes does not have %q", p.ID, "."+suffix)
			}
		}
	}
	for _, suffix := range awsDNSSuffixes {
		if !slices.Contains(want, suffix) {
			t.Errorf("awsDNSSuffixes has %q, which is in no partition of the SDK", suffix)
		}
	}
}

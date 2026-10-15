package fleetdata_test

import (
	"slices"
	"strings"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

func TestAMIParameter(t *testing.T) {
	tests := []struct {
		amiType string
		want    string
	}{
		{"AL2_x86_64", "/aws/service/eks/optimized-ami/1.33/amazon-linux-2/recommended/image_id"},
		{"AL2_ARM_64", "/aws/service/eks/optimized-ami/1.33/amazon-linux-2-arm64/recommended/image_id"},
		{"AL2023_x86_64_STANDARD", "/aws/service/eks/optimized-ami/1.33/amazon-linux-2023/x86_64/standard/recommended/image_id"},
		{"AL2023_ARM_64_NVIDIA", "/aws/service/eks/optimized-ami/1.33/amazon-linux-2023/arm64/nvidia/recommended/image_id"},
		{"BOTTLEROCKET_x86_64", "/aws/service/bottlerocket/aws-k8s-1.33/x86_64/latest/image_id"},
		{"BOTTLEROCKET_ARM_64_NVIDIA", "/aws/service/bottlerocket/aws-k8s-1.33-nvidia/arm64/latest/image_id"},
		{"BOTTLEROCKET_x86_64_FIPS", "/aws/service/bottlerocket/aws-k8s-1.33-fips/x86_64/latest/image_id"},
		{"WINDOWS_CORE_2022_x86_64", "/aws/service/ami-windows-latest/Windows_Server-2022-English-Core-EKS_Optimized-1.33/image_id"},
		{"WINDOWS_FULL_2025_x86_64", "/aws/service/ami-windows-latest/Windows_Server-2025-English-Full-EKS_Optimized-1.33/image_id"},
		// A type newer than the table gets the base variant of its family.
		{"AL2023_x86_64_SOMETHING_NEW", "/aws/service/eks/optimized-ami/1.33/amazon-linux-2023/x86_64/standard/recommended/image_id"},
		{"BOTTLEROCKET_ARM_64_NEW", "/aws/service/bottlerocket/aws-k8s-1.33/arm64/latest/image_id"},
		// No parameter: the user manages the AMI, or the family is not
		// known. A guess would compare with the AMI of another family.
		{"CUSTOM", ""},
		{"UBUNTU_24_x86_64", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got, ok := fleetdata.AMIParameter("1.33", tt.amiType)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("AMIParameter(1.33, %q) = %q, %v, want %q", tt.amiType, got, ok, tt.want)
		}
	}
	if _, ok := fleetdata.AMIParameter("", "AL2_x86_64"); ok {
		t.Error("AMIParameter without a version returned a path")
	}
}

// Every AMI type that the SDK knows has a parameter, CUSTOM apart, and the
// version is in its path.
func TestAMIParameter_EveryTypeOfTheSDK(t *testing.T) {
	known := fleetdata.AMITypes()
	for _, amiType := range ekstypes.AMITypes("").Values() {
		if amiType == ekstypes.AMITypesCustom {
			continue
		}
		if !slices.Contains(known, string(amiType)) {
			t.Errorf("the table has no row for the AMI type %s", amiType)
		}
		path, ok := fleetdata.AMIParameter("1.33", string(amiType))
		if !ok || !strings.Contains(path, "1.33") || !strings.HasSuffix(path, "/image_id") {
			t.Errorf("AMIParameter(1.33, %s) = %q, %v", amiType, path, ok)
		}
	}
}

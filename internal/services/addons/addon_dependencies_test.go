package addons

import (
	"slices"
	"testing"
)

func TestSortByDependency_VPCCNIFirst(t *testing.T) {
	input := []AddonSummary{
		{Name: "coredns"},
		{Name: "kube-proxy"},
		{Name: "vpc-cni"},
	}
	ordered := sortByDependency(input)
	vpcIdx := indexByName(ordered, "vpc-cni")
	coreDNSIdx := indexByName(ordered, "coredns")
	kubeProxyIdx := indexByName(ordered, "kube-proxy")

	if vpcIdx == -1 || coreDNSIdx == -1 || kubeProxyIdx == -1 {
		t.Fatal("expected all addons in output")
	}
	if vpcIdx > coreDNSIdx {
		t.Errorf("vpc-cni (idx %d) must come before coredns (idx %d)", vpcIdx, coreDNSIdx)
	}
	if vpcIdx > kubeProxyIdx {
		t.Errorf("vpc-cni (idx %d) must come before kube-proxy (idx %d)", vpcIdx, kubeProxyIdx)
	}
}

func TestSortByDependency_StorageAfterCoreDNS(t *testing.T) {
	input := []AddonSummary{
		{Name: "aws-ebs-csi-driver"},
		{Name: "coredns"},
		{Name: "vpc-cni"},
	}
	ordered := sortByDependency(input)
	coreDNSIdx := indexByName(ordered, "coredns")
	ebsIdx := indexByName(ordered, "aws-ebs-csi-driver")

	if coreDNSIdx == -1 || ebsIdx == -1 {
		t.Fatal("expected all addons in output")
	}
	if coreDNSIdx > ebsIdx {
		t.Errorf("coredns (idx %d) must come before aws-ebs-csi-driver (idx %d)", coreDNSIdx, ebsIdx)
	}
}

func TestSortByDependency_UnknownAddonAppended(t *testing.T) {
	input := []AddonSummary{
		{Name: "my-custom-addon"},
		{Name: "vpc-cni"},
	}
	ordered := sortByDependency(input)
	if len(ordered) != 2 {
		t.Fatalf("expected 2 addons, got %d", len(ordered))
	}
}

func TestSortByDependency_MissingDepSkipped(t *testing.T) {
	// coredns depends on vpc-cni, but vpc-cni is not in the list
	input := []AddonSummary{
		{Name: "coredns"},
		{Name: "kube-proxy"},
	}
	ordered := sortByDependency(input)
	if len(ordered) != 2 {
		t.Fatalf("expected 2 addons, got %d: %v", len(ordered), addonNames(ordered))
	}
}

func TestSortByDependency_PreservesAll(t *testing.T) {
	input := []AddonSummary{
		{Name: "aws-ebs-csi-driver"},
		{Name: "coredns"},
		{Name: "kube-proxy"},
		{Name: "vpc-cni"},
		{Name: "amazon-cloudwatch-observability"},
	}
	ordered := sortByDependency(input)
	if len(ordered) != len(input) {
		t.Errorf("expected %d addons, got %d", len(input), len(ordered))
	}
	names := addonNames(ordered)
	for _, a := range input {
		if !slices.Contains(names, a.Name) {
			t.Errorf("addon %q missing from output", a.Name)
		}
	}
}

func TestSortByDependency_EmptyInput(t *testing.T) {
	ordered := sortByDependency(nil)
	if len(ordered) != 0 {
		t.Errorf("expected empty slice, got %v", ordered)
	}
}

func TestAddonNames(t *testing.T) {
	addons := []AddonSummary{{Name: "a"}, {Name: "b"}}
	got := addonNames(addons)
	want := []string{"a", "b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func indexByName(addons []AddonSummary, name string) int {
	for i, a := range addons {
		if a.Name == name {
			return i
		}
	}
	return -1
}

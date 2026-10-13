package change

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Two requests that differ in one field are not the same request, so a
// plan of one is no consent to the other.
func TestUpgradeRequestDiffers(t *testing.T) {
	base := UpgradeRequest{Target: Target{Region: east, Cluster: "prod"}, To: ver("1.33")}
	tests := []struct {
		field string
		edit  func(r *UpgradeRequest)
	}{
		{"the cluster", func(r *UpgradeRequest) { r.Cluster = "dev" }},
		{"--to", func(r *UpgradeRequest) { r.To = ver("1.34") }},
		{"--only", func(r *UpgradeRequest) { r.Only = []Part{PartAddons} }},
		{"--nodegroup", func(r *UpgradeRequest) { r.Nodegroups = []string{"a"} }},
		{"--skip-addon", func(r *UpgradeRequest) { r.SkipAddons = []string{"a"} }},
		{"--skip-nodegroup", func(r *UpgradeRequest) { r.SkipNodegroups = []string{"a"} }},
		{"--skip-insights-check", func(r *UpgradeRequest) { r.SkipInsights = true }},
		{"--force", func(r *UpgradeRequest) { r.Policy.Force = true }},
	}
	reviewed := &Plan{Kind: ClusterUpgrade, Request: base}
	if err := sameRequest(reviewed, base); err != nil {
		t.Fatalf("the same request: %v", err)
	}
	for _, tt := range tests {
		other := base
		tt.edit(&other)
		if err := sameRequest(reviewed, other); !errors.Is(err, ErrOtherRequest) || !strings.Contains(err.Error(), tt.field+" differs") {
			t.Errorf("%s: err = %v, want ErrOtherRequest that names it", tt.field, err)
		}
	}
	if err := sameRequest(reviewed, RollbackRequest{Target: base.Target}); !errors.Is(err, ErrOtherRequest) {
		t.Errorf("a rollback against the plan of an upgrade: err = %v", err)
	}
}

func TestRollbackRequestDiffers(t *testing.T) {
	base := RollbackRequest{Target: Target{Region: east, Cluster: "prod"}}
	reviewed := &Plan{Kind: ClusterRollback, Request: base}
	if err := sameRequest(reviewed, base); err != nil {
		t.Fatalf("the same request: %v", err)
	}
	for field, edit := range map[string]func(r *RollbackRequest){
		"the region":            func(r *RollbackRequest) { r.Region = "eu-west-1" },
		"--skip-nodegroup":      func(r *RollbackRequest) { r.SkipNodegroups = []string{"a"} },
		"--skip-insights-check": func(r *RollbackRequest) { r.SkipInsights = true },
		"--rollback-timeout":    func(r *RollbackRequest) { r.RollbackTimeout = 3 * time.Hour },
		"--yes":                 func(r *RollbackRequest) { r.Policy.Yes = true },
	} {
		other := base
		edit(&other)
		if err := sameRequest(reviewed, other); !errors.Is(err, ErrOtherRequest) || !strings.Contains(err.Error(), field+" differs") {
			t.Errorf("%s: err = %v, want ErrOtherRequest that names it", field, err)
		}
	}
}

func TestValidateRollbackTimeout(t *testing.T) {
	for d, ok := range map[time.Duration]bool{
		0: true, 2 * time.Hour: true, 168 * time.Hour: true, 12 * time.Hour: true,
		time.Hour: false, 169 * time.Hour: false, 2*time.Hour + time.Second: false, -2 * time.Hour: false,
	} {
		err := ValidateRollbackTimeout(d)
		if (err == nil) != ok {
			t.Errorf("ValidateRollbackTimeout(%s) = %v, want ok=%v", d, err, ok)
		}
		var refusal *RefusalError
		if err != nil && (!errors.As(err, &refusal) || refusal.Reason != RefusalRollbackTimeout) {
			t.Errorf("ValidateRollbackTimeout(%s) = %v, want a refusal for RollbackTimeout", d, err)
		}
	}
}

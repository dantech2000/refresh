//go:build livecheck

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// TestLiveOutputMatchesSchemas builds the refresh binary of this tree
// (./cmd/refresh), runs its read commands against the account with -o json
// and -o yaml, and validates each document against the schema of its kind
// in api/schema/v2. A command may exit 2 or 4 and still print a document;
// a run with no document on stdout fails the test.
//
// The commands read only, with calls that cost nothing; a dry run sends no
// change. They use the profile and the kubeconfig of the environment. The
// calls, from awsx.RequiredPermissions:
//
//   - every sweep: sts:GetCallerIdentity, only when no region answered
//   - status: eks:ListClusters, eks:DescribeCluster,
//     eks:DescribeClusterVersions, eks:ListNodegroups,
//     eks:DescribeNodegroup, eks:ListAddons, eks:DescribeAddon,
//     eks:DescribeAddonVersions, ssm:GetParameter, ec2:DescribeImages,
//     ec2:DescribeInstances, ec2:DescribeLaunchTemplateVersions,
//     autoscaling:DescribeAutoScalingGroups
//   - cluster list: eks:ListClusters, eks:DescribeCluster
//   - nodegroup update --all-clusters --dry-run: the reads of status, and
//     those of the health gate and the availability check:
//     cloudwatch:GetMetricData, servicequotas:GetServiceQuota,
//     ec2:DescribeSubnets, ec2:DescribeInstanceTypeOfferings, and the
//     Kubernetes reads of nodes, pods, and PodDisruptionBudgets where a
//     kubeconfig context matches the cluster
//   - version: none
func TestLiveOutputMatchesSchemas(t *testing.T) {
	// The binary finds the credentials itself. This fails at once when
	// there are none, before the build.
	awsxtest.LiveConfig(t, "")
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "refresh")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/refresh")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building ./cmd/refresh: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"version", "-o", "json"},
		{"status", "-o", "json"},
		{"status", "-o", "yaml"},
		{"status", "-A", "-o", "json"},
		{"status", "-r", "us-east-1", "-r", "af-south-1", "-o", "json"},
		{"cluster", "list", "-o", "json"},
		{"cluster", "list", "-A", "-o", "yaml"},
		{"nodegroup", "update", "--all-clusters", "--dry-run", "-o", "json"},
		{"nodegroup", "update", "--all-clusters", "--dry-run", "-r", "us-east-1,us-west-2", "-o", "yaml"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code := 0
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("running refresh: %v", err)
				}
				code = exit.ExitCode()
			}
			document := stdout.Bytes()
			if args[len(args)-1] == "yaml" {
				var v any
				if err := yaml.Unmarshal(document, &v); err != nil {
					t.Fatalf("exit %d, stdout is not YAML: %v\n%s\nstderr: %s", code, err, document, stderr.String())
				}
				if document, err = json.Marshal(v); err != nil {
					t.Fatalf("exit %d, the YAML document has no JSON form: %v", code, err)
				}
			}
			var head struct {
				Kind apidoc.Kind `json:"kind"`
			}
			if err := json.Unmarshal(document, &head); err != nil || head.Kind == "" {
				t.Fatalf("exit %d, stdout is not one refresh document (%v):\n%s\nstderr: %s", code, err, stdout.String(), stderr.String())
			}
			if !slices.Contains(apidoc.Kinds(), head.Kind) {
				t.Fatalf("exit %d, the document has the unknown kind %q", code, head.Kind)
			}
			cmdutiltest.ValidateJSON(t, head.Kind, document)
			t.Logf("exit %d, %s; stderr: %q", code, head.Kind, firstLine(stderr.String()))
		})
	}
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

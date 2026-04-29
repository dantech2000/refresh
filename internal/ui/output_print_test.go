package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/types"
)

func captureOutputStreams(t *testing.T, fn func()) (string, string) {
	t.Helper()
	origOut := os.Stdout
	origErr := os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = outW
	os.Stderr = errW
	t.Cleanup(func() {
		os.Stdout = origOut
		os.Stderr = origErr
	})

	fn()
	_ = outW.Close()
	_ = errW.Close()

	var stdout, stderr bytes.Buffer
	_, _ = io.Copy(&stdout, outR)
	_, _ = io.Copy(&stderr, errR)
	return stdout.String(), stderr.String()
}

func TestPrintHelpers(t *testing.T) {
	stdout, stderr := captureOutputStreams(t, func() {
		Outln("hello")
		Outf("%s", " world")
		Errln("bad")
		Errf("%s", " wolf")
	})

	if !strings.Contains(stdout, "hello") || !strings.Contains(stdout, "world") {
		t.Fatalf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "bad") || !strings.Contains(stderr, "wolf") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestPrintNodegroupsTree(t *testing.T) {
	stdout, _ := captureOutputStreams(t, func() {
		PrintNodegroupsTree("cluster", []types.NodegroupInfo{
			{Name: "ng-1", Status: "ACTIVE", InstanceType: "m5.large", Desired: 2, CurrentAmi: "ami-1", AmiStatus: types.AMILatest},
			{Name: "ng-2", Status: "UPDATING", InstanceType: "m5.xlarge", Desired: 3, AmiStatus: types.AMIUnknown},
		})
	})

	for _, want := range []string{"cluster", "ng-1", "ng-2", "ami-1", "Unknown"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("PrintNodegroupsTree output missing %q: %q", want, stdout)
		}
	}
}

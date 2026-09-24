package diag_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

func TestWithOperation(t *testing.T) {
	if diag.WithOperation(diag.OpListNodegroups, nil) != nil {
		t.Error("WithOperation(nil) != nil")
	}
	base := mocks.Throttling()
	err := fmt.Errorf("listing nodegroups: %w", diag.WithOperation(diag.OpListNodegroups, base))
	if got := diag.OperationOf(err); got != diag.OpListNodegroups {
		t.Errorf("OperationOf = %q, want %q", got, diag.OpListNodegroups)
	}
	if err.Error() != "listing nodegroups: "+base.Error() {
		t.Errorf("the tag changed the text: %q", err)
	}
	if !errors.Is(err, base) {
		t.Error("errors.Is does not see through the tag")
	}
	f := diag.FromError(diag.KindCluster, "prod", "", err)
	if f.Operation != diag.OpListNodegroups || f.Reason != diag.ReasonThrottled {
		t.Errorf("FromError = %+v, want the tagged operation and Throttled", f)
	}
	// An explicit op wins over the tag.
	if f := diag.FromError(diag.KindCluster, "prod", diag.OpDescribeCluster, err); f.Operation != diag.OpDescribeCluster {
		t.Errorf("explicit op: Operation = %q", f.Operation)
	}
	if diag.OperationOf(base) != "" {
		t.Error("an untagged error has an operation")
	}
}

package addons

import (
	"errors"
	"fmt"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
)

// updateEndedError is an EKS add-on update that ended Failed or Cancelled.
type updateEndedError struct {
	addon, updateID string
	status          ekstypes.UpdateStatus
	details         string
}

func (e *updateEndedError) Error() string {
	return fmt.Sprintf("addon %s update %s %s%s", e.addon, e.updateID, e.status, e.details)
}

// UpdateFailure is updateFailure for other packages, such as the upgrade
// orchestrator's addon phase.
func UpdateFailure(kind diag.Kind, cluster, addon, updateID string, err error) *diag.Failure {
	return updateFailure(kind, cluster, addon, updateID, err)
}

// updateFailure builds the failure of addon's update on cluster from err.
// kind is KindAddon for a failure to read or submit, KindUpdate for a
// submitted update that did not complete. An update that ended Failed or
// Cancelled gets ReasonUpdateFailed or ReasonUpdateCancelled, and no
// version in the catalog gets ReasonNotFound; any other error is classified
// by type, with the IAM action from its diag.WithOperation tag.
func updateFailure(kind diag.Kind, cluster, addon, updateID string, err error) *diag.Failure {
	var f diag.Failure
	var ended *updateEndedError
	switch {
	case errors.As(err, &ended):
		reason := diag.ReasonUpdateFailed
		if ended.status == ekstypes.UpdateStatusCancelled {
			reason = diag.ReasonUpdateCancelled
		}
		f = diag.New(diag.KindUpdate, addon, reason, err.Error())
	case errors.Is(err, ErrNoVersionsFound):
		f = diag.New(kind, addon, diag.ReasonNotFound, err.Error())
		f.Operation = diag.OpDescribeAddonVersions
	default:
		// The IAM action comes from the error's diag.WithOperation tag.
		f = diag.FromError(kind, addon, "", err)
	}
	f.Cluster, f.UpdateID = cluster, updateID
	return &f
}

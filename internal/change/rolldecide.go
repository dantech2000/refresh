package change

import "github.com/dantech2000/refresh/internal/fleet"

// amiTypeCustom is the AMI type of a nodegroup whose launch template
// names the AMI. EKS cannot pick a recommended AMI for it.
const amiTypeCustom = "CUSTOM"

// rollDecision is what a roll does with one nodegroup, and why.
type rollDecision struct {
	Action Action
	Reason Reason
}

// decideAMIUpdate is the one decision table of a nodegroup roll. The plan
// of a dry run and the plan of a real run both come from it, so a dry run
// names the action that the real run takes. It is a pure function of the
// nodegroup as the snapshot read it. The first row that matches decides.
//
//  1. The AMI type is CUSTOM: Manual, CustomAMI. EKS cannot roll it. The
//     user publishes a new launch template version.
//  2. The nodegroup is already changing (UPDATING, CREATING, DELETING):
//     Skip, InFlight. The target is busy.
//  3. The AMI type is one that this version of refresh does not know, or
//     EKS returned none, so there is no recommended AMI to compare with:
//     Skip, AMITypeUnknown. A roll would be sent again on every run. The
//     user upgrades refresh.
//  4. The current or the latest AMI is not known: Submit, AMIUnknown. A
//     nodegroup that may be behind is rolled, and never skipped as up to
//     date.
//  5. The current AMI is the latest, with reroll: Submit, Reroll.
//  6. The current AMI is the latest: Skip, AlreadyLatest.
//  7. Otherwise: Submit, AMIOutdated.
//
// No row reads --force. --force goes past drain blockers and does nothing
// else: it never rolls a nodegroup that is on the latest AMI, and it
// never rolls a custom AMI. Only reroll does the first.
func decideAMIUpdate(ng fleet.Nodegroup, reroll bool) rollDecision {
	current, latest := "", ""
	if ng.AMI.State == fleet.Collected {
		current, latest = ng.AMI.Current, ng.AMI.Latest
	}
	switch {
	case ng.AMIType == amiTypeCustom:
		return rollDecision{Action: Manual, Reason: CustomAMI}
	case nodegroupInFlight(ng.Status):
		return rollDecision{Action: Skip, Reason: InFlight}
	case !fleet.KnownAMIType(ng.AMIType):
		return rollDecision{Action: Skip, Reason: AMITypeUnknown}
	case current == "" || latest == "":
		return rollDecision{Action: Submit, Reason: AMIUnknown}
	case current == latest && reroll:
		return rollDecision{Action: Submit, Reason: Reroll}
	case current == latest:
		return rollDecision{Action: Skip, Reason: AlreadyLatest}
	}
	return rollDecision{Action: Submit, Reason: AMIOutdated}
}

// redecideRoll decides again about a roll that the plan would submit, with
// the fresh read of the nodegroup before the request. fromRelease is the
// AMI release version that the plan read. changed is false when the
// decision of the plan stands, and d is then the zero value. The plan is minutes old by then: the gate ran, a person
// answered, and the clusters before this one were rolled.
//
// The fresh read is one DescribeNodegroup, so it has the AMI type and the
// AMI release version, and not the AMI of each instance. The first row
// that matches decides.
//
//  1. The AMI type is CUSTOM now: Manual, CustomAMI.
//  2. The release version is not the one that the plan read: Skip,
//     AlreadyLatest. Someone else rolled the nodegroup since the plan, and
//     a second roll would replace nodes that were just replaced. That
//     holds for a reroll too: it was planned for the nodes that the plan
//     saw, and they are gone.
//  3. Otherwise: the decision of the plan stands.
//
// The caller handles a nodegroup that is changing, that is not ACTIVE, or
// that has another Kubernetes version before it asks: see submitRoll.
func redecideRoll(fromRelease string, fresh settledResource) (d rollDecision, changed bool) {
	switch {
	case fresh.AMIType == amiTypeCustom:
		return rollDecision{Action: Manual, Reason: CustomAMI}, true
	case fresh.Release != fromRelease:
		return rollDecision{Action: Skip, Reason: AlreadyLatest}, true
	}
	return rollDecision{}, false
}

package change

import "github.com/dantech2000/refresh/internal/fleet"

// amiTypeCustom is the AMI type of a nodegroup whose launch template
// names the AMI. EKS cannot pick a recommended AMI for it.
const amiTypeCustom = "CUSTOM"

// RollDecision is what a roll does with one nodegroup, and why.
type RollDecision struct {
	Action Action
	Reason Reason
}

// DecideAMIUpdate is the one decision table of a nodegroup roll. The plan
// of a dry run and the plan of a real run both come from it, so a dry run
// names the action that the real run takes. It is a pure function of the
// nodegroup as the snapshot read it. The first row that matches decides.
//
//  1. The AMI type is CUSTOM: Manual, CustomAMI. EKS cannot roll it. The
//     user publishes a new launch template version.
//  2. The nodegroup is already changing (UPDATING, CREATING, DELETING):
//     Skip, InFlight. The target is busy.
//  3. The current or the latest AMI is not known: Submit, AMIUnknown. A
//     nodegroup that may be behind is rolled, and never skipped as up to
//     date.
//  4. The current AMI is the latest, with reroll: Submit, Reroll.
//  5. The current AMI is the latest: Skip, AlreadyLatest.
//  6. Otherwise: Submit, AMIOutdated.
//
// No row reads --force. --force goes past drain blockers and does nothing
// else: it never rolls a nodegroup that is on the latest AMI, and it
// never rolls a custom AMI. Only reroll does the first.
func DecideAMIUpdate(ng fleet.Nodegroup, reroll bool) RollDecision {
	current, latest := "", ""
	if ng.AMI.State == fleet.Collected {
		current, latest = ng.AMI.Current, ng.AMI.Latest
	}
	switch {
	case ng.AMIType == amiTypeCustom:
		return RollDecision{Action: Manual, Reason: CustomAMI}
	case nodegroupInFlight(ng.Status):
		return RollDecision{Action: Skip, Reason: InFlight}
	case current == "" || latest == "":
		return RollDecision{Action: Submit, Reason: AMIUnknown}
	case current == latest && reroll:
		return RollDecision{Action: Submit, Reason: Reroll}
	case current == latest:
		return RollDecision{Action: Skip, Reason: AlreadyLatest}
	}
	return RollDecision{Action: Submit, Reason: AMIOutdated}
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
func redecideRoll(fromRelease string, fresh Settled) (d RollDecision, changed bool) {
	switch {
	case fresh.AMIType == amiTypeCustom:
		return RollDecision{Action: Manual, Reason: CustomAMI}, true
	case fresh.Release != fromRelease:
		return RollDecision{Action: Skip, Reason: AlreadyLatest}, true
	}
	return RollDecision{}, false
}

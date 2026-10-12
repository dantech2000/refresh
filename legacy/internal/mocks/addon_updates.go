package mocks

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// SettleAddonUpdates makes add-on updates land, as they do in EKS: after a
// successful UpdateAddon, DescribeAddon reports the requested version (status
// ACTIVE), and DescribeUpdate reports the update Successful unless a
// WithUpdateStatuses script (or a custom DescribeUpdateFn) is already set.
//
// It wraps the current UpdateAddonFn, DescribeAddonFn, and DescribeUpdateFn,
// so call it after every Fn override on m.
func SettleAddonUpdates(m *EKSAPI) {
	var mu sync.Mutex
	installed := map[string]string{} // addon name -> version set by UpdateAddon

	prevUpdate := m.UpdateAddonFn
	m.UpdateAddonFn = func(ctx context.Context, in *eks.UpdateAddonInput, opts ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		if prevUpdate == nil {
			return nil, &ekstypes.InvalidRequestException{Message: aws.String("mocks: UpdateAddonFn is not set")}
		}
		out, err := prevUpdate(ctx, in, opts...)
		if err == nil && in.AddonVersion != nil {
			mu.Lock()
			installed[aws.ToString(in.AddonName)] = aws.ToString(in.AddonVersion)
			mu.Unlock()
		}
		return out, err
	}

	prevDescribe := m.DescribeAddonFn
	m.DescribeAddonFn = func(ctx context.Context, in *eks.DescribeAddonInput, opts ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		if prevDescribe == nil {
			return nil, notFound("No addon: " + aws.ToString(in.AddonName))
		}
		out, err := prevDescribe(ctx, in, opts...)
		if err != nil || out == nil || out.Addon == nil {
			return out, err
		}
		mu.Lock()
		v, ok := installed[aws.ToString(in.AddonName)]
		mu.Unlock()
		if ok {
			addon := *out.Addon
			addon.AddonVersion = aws.String(v)
			addon.Status = ekstypes.AddonStatusActive
			return &eks.DescribeAddonOutput{Addon: &addon}, nil
		}
		return out, nil
	}

	if m.DescribeUpdateFn == nil {
		m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
			return &eks.DescribeUpdateOutput{
				Update: &ekstypes.Update{Id: in.UpdateId, Status: ekstypes.UpdateStatusSuccessful},
			}, nil
		}
	}
}

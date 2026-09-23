package mocks

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// pageTokenPrefix marks tokens this mock issued. EKS tokens are opaque; the
// mock encodes the next offset so a stale or foreign token is detectable.
const pageTokenPrefix = "mock-page-"

// pageSlice returns the page of items that token points at, at most size
// long, and the token for the following page (nil on the last page). A nil
// token starts at the beginning. An unknown or out-of-range token fails with
// InvalidParameterException, as EKS does.
func pageSlice[T any](items []T, token *string, size int) ([]T, *string, error) {
	offset := 0
	if token != nil {
		raw, ok := strings.CutPrefix(*token, pageTokenPrefix)
		n, err := strconv.Atoi(raw)
		if !ok || err != nil || n < 0 || n > len(items) {
			return nil, nil, invalidParameter(fmt.Sprintf("The nextToken %q is not valid.", *token))
		}
		offset = n
	}
	end := min(offset+size, len(items))
	page := items[offset:end]
	if end >= len(items) {
		return page, nil, nil
	}
	return page, aws.String(pageTokenPrefix + strconv.Itoa(end)), nil
}

// The page* helpers run when EKSAPI.PageSize > 0. Each asks the configured Fn
// for the full, unpaged result (NextToken cleared), then returns only the
// page the caller's token selects. A Fn that already returns its own
// NextToken is passed through untouched, so hand-written paging still works.

func (m *EKSAPI) pageListClusters(in *eks.ListClustersInput, out *eks.ListClustersOutput) (*eks.ListClustersOutput, error) {
	if out.NextToken != nil {
		return out, nil
	}
	page, next, err := pageSlice(out.Clusters, in.NextToken, m.PageSize)
	if err != nil {
		return nil, err
	}
	cp := *out
	cp.Clusters, cp.NextToken = page, next
	return &cp, nil
}

func (m *EKSAPI) pageListAddons(in *eks.ListAddonsInput, out *eks.ListAddonsOutput) (*eks.ListAddonsOutput, error) {
	if out.NextToken != nil {
		return out, nil
	}
	page, next, err := pageSlice(out.Addons, in.NextToken, m.PageSize)
	if err != nil {
		return nil, err
	}
	cp := *out
	cp.Addons, cp.NextToken = page, next
	return &cp, nil
}

func (m *EKSAPI) pageListNodegroups(in *eks.ListNodegroupsInput, out *eks.ListNodegroupsOutput) (*eks.ListNodegroupsOutput, error) {
	if out.NextToken != nil {
		return out, nil
	}
	page, next, err := pageSlice(out.Nodegroups, in.NextToken, m.PageSize)
	if err != nil {
		return nil, err
	}
	cp := *out
	cp.Nodegroups, cp.NextToken = page, next
	return &cp, nil
}

func (m *EKSAPI) pageListInsights(in *eks.ListInsightsInput, out *eks.ListInsightsOutput) (*eks.ListInsightsOutput, error) {
	if out.NextToken != nil {
		return out, nil
	}
	page, next, err := pageSlice(out.Insights, in.NextToken, m.PageSize)
	if err != nil {
		return nil, err
	}
	cp := *out
	cp.Insights, cp.NextToken = page, next
	return &cp, nil
}

// addonVersionEntry is one (addon, version) pair of a DescribeAddonVersions
// result. version is nil for an addon listed with no versions.
type addonVersionEntry struct {
	addon   int
	version *ekstypes.AddonVersionInfo
}

// pageDescribeAddonVersions pages over (addon, version) pairs rather than
// whole addons: callers filter by AddonName, so a result has one addon and
// paging whole addons would never split it. Each page regroups its pairs
// under their AddonInfo, so a caller that reads only the first page sees an
// incomplete version list, as it would against a large real catalogue.
func (m *EKSAPI) pageDescribeAddonVersions(in *eks.DescribeAddonVersionsInput, out *eks.DescribeAddonVersionsOutput) (*eks.DescribeAddonVersionsOutput, error) {
	if out.NextToken != nil {
		return out, nil
	}
	var entries []addonVersionEntry
	for i := range out.Addons {
		if len(out.Addons[i].AddonVersions) == 0 {
			entries = append(entries, addonVersionEntry{addon: i})
			continue
		}
		for j := range out.Addons[i].AddonVersions {
			entries = append(entries, addonVersionEntry{addon: i, version: &out.Addons[i].AddonVersions[j]})
		}
	}
	page, next, err := pageSlice(entries, in.NextToken, m.PageSize)
	if err != nil {
		return nil, err
	}
	cp := *out
	cp.Addons, cp.NextToken = nil, next
	for _, e := range page {
		if n := len(cp.Addons); n == 0 || aws.ToString(cp.Addons[n-1].AddonName) != aws.ToString(out.Addons[e.addon].AddonName) {
			info := out.Addons[e.addon]
			info.AddonVersions = nil
			cp.Addons = append(cp.Addons, info)
		}
		if e.version != nil {
			last := &cp.Addons[len(cp.Addons)-1]
			last.AddonVersions = append(last.AddonVersions, *e.version)
		}
	}
	return &cp, nil
}

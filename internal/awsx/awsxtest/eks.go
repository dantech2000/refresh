package awsxtest

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The path parameters carry the member names of the EKS model, which names
// the cluster "name" in some operations and "clusterName" in others.
var eksRoutes = []route{
	{http.MethodGet, "/clusters", "ListClusters"},
	{http.MethodGet, "/clusters/{name}", "DescribeCluster"},
	{http.MethodGet, "/cluster-versions", "DescribeClusterVersions"},
	{http.MethodGet, "/addons/supported-versions", "DescribeAddonVersions"},
	{http.MethodGet, "/clusters/{clusterName}/node-groups", "ListNodegroups"},
	{http.MethodGet, "/clusters/{clusterName}/node-groups/{nodegroupName}", "DescribeNodegroup"},
	{http.MethodPost, "/clusters/{clusterName}/node-groups/{nodegroupName}/update-version", "UpdateNodegroupVersion"},
	{http.MethodPost, "/clusters/{clusterName}/node-groups/{nodegroupName}/update-config", "UpdateNodegroupConfig"},
	{http.MethodGet, "/clusters/{clusterName}/addons", "ListAddons"},
	{http.MethodGet, "/clusters/{clusterName}/addons/{addonName}", "DescribeAddon"},
	{http.MethodPost, "/clusters/{clusterName}/addons/{addonName}/update", "UpdateAddon"},
	{http.MethodPost, "/clusters/{clusterName}/insights", "ListInsights"},
	{http.MethodGet, "/clusters/{clusterName}/insights/{id}", "DescribeInsight"},
	{http.MethodPost, "/clusters/{clusterName}/insights-refresh", "StartInsightsRefresh"},
	{http.MethodGet, "/clusters/{clusterName}/insights-refresh", "DescribeInsightsRefresh"},
	{http.MethodPost, "/clusters/{name}/updates", "UpdateClusterVersion"},
	{http.MethodGet, "/clusters/{name}/updates", "ListUpdates"},
	{http.MethodGet, "/clusters/{name}/updates/{updateId}", "DescribeUpdate"},
}

var eksOperations = map[string]operation{
	"ListClusters":            {handle: eksListClusters},
	"DescribeCluster":         {handle: eksDescribeCluster},
	"DescribeClusterVersions": {handle: eksDescribeClusterVersions},
	"DescribeAddonVersions":   {handle: eksDescribeAddonVersions},
	"ListNodegroups":          {handle: eksListNodegroups},
	"DescribeNodegroup":       {handle: eksDescribeNodegroup},
	"UpdateNodegroupVersion":  {handle: eksUpdateNodegroupVersion, mutates: true},
	"UpdateNodegroupConfig":   {handle: eksUpdateNodegroupConfig, mutates: true},
	"ListAddons":              {handle: eksListAddons},
	"DescribeAddon":           {handle: eksDescribeAddon},
	"UpdateAddon":             {handle: eksUpdateAddon, mutates: true},
	"ListInsights":            {handle: eksListInsights},
	"DescribeInsight":         {handle: eksDescribeInsight},
	"StartInsightsRefresh":    {handle: eksStartInsightsRefresh, mutates: true},
	"DescribeInsightsRefresh": {handle: eksDescribeInsightsRefresh},
	"UpdateClusterVersion":    {handle: eksUpdateClusterVersion, mutates: true},
	"ListUpdates":             {handle: eksListUpdates},
	"DescribeUpdate":          {handle: eksDescribeUpdate},
}

type object = map[string]any

func eksNotFound(message string) *apiError {
	return newError("ResourceNotFoundException", message)
}

// eksError words an error of the world as EKS does.
func eksError(err error) error {
	var notFound *fleettest.NotFoundError
	var inUse *fleettest.InUseError
	var invalid *fleettest.InvalidError
	switch {
	case errors.As(err, &notFound):
		switch notFound.Kind {
		case "nodegroup":
			return eksNotFound("No node group found for name: " + notFound.Name + ".")
		case "addon":
			return eksNotFound("No addon: " + notFound.Name)
		}
		return eksNotFound("No cluster found for name: " + notFound.Name + ".")
	case errors.As(err, &inUse):
		return newError("ResourceInUseException", "Cannot update "+inUse.Name+" because it has update "+inUse.UpdateID+" in progress")
	case errors.As(err, &invalid):
		return newError("InvalidParameterException", invalid.Message)
	}
	return err
}

func (c *call) clusterName() string { return c.in.clusterName() }

// inCluster runs f on the request's cluster with the world locked, and
// encodes what f returns before the lock is released. The answer is then
// bytes: it shares no map or slice with the world, which a concurrent Edit
// may change while the response is written.
func (c *call) inCluster(f func(s *fleettest.State, cl *fleettest.Cluster) (any, error)) (any, error) {
	var out json.RawMessage
	var err error
	c.world.View(func(s *fleettest.State) {
		cl := s.Cluster(c.region, c.clusterName())
		if cl == nil {
			err = eksError(&fleettest.NotFoundError{Kind: "cluster", Name: c.clusterName()})
			return
		}
		var v any
		if v, err = f(s, cl); err == nil {
			out, err = json.Marshal(v)
		}
	})
	return out, err
}

// paged answers a list under key with the EKS paging members.
func paged[T any](c *call, key string, items []T) (any, error) {
	items, next, err := page(c, items, "maxResults", "nextToken")
	if err != nil {
		return nil, err
	}
	out := object{key: append([]T{}, items...)}
	if next != "" {
		out["nextToken"] = next
	}
	return out, nil
}

func names[T any](items []T, name func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, name(it))
	}
	return out
}

func eksListClusters(c *call) (any, error) {
	var list []string
	c.world.View(func(s *fleettest.State) {
		for _, cl := range s.Clusters {
			if cl.InRegion(c.region) {
				list = append(list, cl.Name)
			}
		}
	})
	slices.Sort(list)
	return paged(c, "clusters", list)
}

func eksDescribeCluster(c *call) (any, error) {
	return c.inCluster(func(s *fleettest.State, cl *fleettest.Cluster) (any, error) {
		return object{"cluster": clusterJSON(s, c.region, cl)}, nil
	})
}

func clusterJSON(s *fleettest.State, region string, cl *fleettest.Cluster) object {
	out := object{
		"name":            cl.Name,
		"arn":             "arn:aws:eks:" + region + ":" + s.Account + ":cluster/" + cl.Name,
		"version":         cl.Version,
		"status":          cl.Status,
		"endpoint":        "https://" + cl.Name + ".eks.fake.invalid",
		"platformVersion": cl.PlatformVersion,
		"createdAt":       epoch(cl.CreatedAt),
		"resourcesVpcConfig": object{
			"vpcId":                 cl.VPCID,
			"subnetIds":             append([]string{}, cl.SubnetIDs...),
			"securityGroupIds":      append([]string{}, cl.SecurityGroupIDs...),
			"endpointPublicAccess":  cl.EndpointPublicAccess,
			"endpointPrivateAccess": cl.EndpointPrivateAccess,
			"publicAccessCidrs":     append([]string{}, cl.PublicAccessCIDRs...),
		},
		"health":             object{"issues": issuesJSON(cl.HealthIssues)},
		"deletionProtection": cl.DeletionProtection,
	}
	if cl.RoleARN != "" {
		out["roleArn"] = cl.RoleARN
	}
	if cl.SupportType != "" {
		out["upgradePolicy"] = object{"supportType": cl.SupportType}
	}
	if cl.AutoMode {
		out["computeConfig"] = object{"enabled": true}
	}
	if len(cl.LogTypes) > 0 {
		out["logging"] = object{"clusterLogging": []object{{"types": cl.LogTypes, "enabled": true}}}
	}
	if cl.EncryptionKeyARN != "" {
		out["encryptionConfig"] = []object{{"resources": []string{"secrets"}, "provider": object{"keyArn": cl.EncryptionKeyARN}}}
	}
	if len(cl.Tags) > 0 {
		out["tags"] = cl.Tags
	}
	return out
}

func issuesJSON(issues []fleettest.Issue) []object {
	out := make([]object, 0, len(issues))
	for _, is := range issues {
		out = append(out, object{"code": is.Code, "message": is.Message, "resourceIds": append([]string{}, is.ResourceIDs...)})
	}
	return out
}

// epoch is a timestamp as the JSON protocols carry it.
func epoch(t time.Time) float64 {
	return float64(t.UnixMilli()) / 1e3
}

func eksDescribeClusterVersions(c *call) (any, error) {
	asked := c.in.strs("clusterVersions")
	var list []object
	c.world.View(func(s *fleettest.State) {
		for _, v := range s.ClusterVersions {
			if !c.sees(v.Region) || len(asked) > 0 && !slices.Contains(asked, v.Version) {
				continue
			}
			o := object{"clusterVersion": v.Version, "versionStatus": v.Status}
			if !v.EndOfStandardSupport.IsZero() {
				o["endOfStandardSupportDate"] = epoch(v.EndOfStandardSupport)
			}
			if !v.EndOfExtendedSupport.IsZero() {
				o["endOfExtendedSupportDate"] = epoch(v.EndOfExtendedSupport)
			}
			list = append(list, o)
		}
	})
	return paged(c, "clusterVersions", list)
}

// eksDescribeAddonVersions answers the catalog, one entry per add-on, in
// the order the add-ons first appear in State.AddonVersions.
func eksDescribeAddonVersions(c *call) (any, error) {
	addon, version := c.in.str("addonName"), c.in.str("kubernetesVersion")
	var order []string
	versions := map[string][]object{}
	c.world.View(func(s *fleettest.State) {
		for _, av := range s.AddonVersions {
			if !c.sees(av.Region) || addon != "" && av.Addon != addon {
				continue
			}
			if version != "" && !slices.Contains(av.ClusterVersions, version) {
				continue
			}
			var compat []object
			for _, cv := range av.ClusterVersions {
				if version == "" || cv == version {
					compat = append(compat, object{"clusterVersion": cv, "defaultVersion": av.Default})
				}
			}
			if _, seen := versions[av.Addon]; !seen {
				order = append(order, av.Addon)
			}
			versions[av.Addon] = append(versions[av.Addon], object{"addonVersion": av.Version, "compatibilities": compat})
		}
	})
	list := make([]object, 0, len(order))
	for _, name := range order {
		list = append(list, object{"addonName": name, "addonVersions": versions[name]})
	}
	return paged(c, "addons", list)
}

func eksListNodegroups(c *call) (any, error) {
	return c.inCluster(func(_ *fleettest.State, cl *fleettest.Cluster) (any, error) {
		return paged(c, "nodegroups", names(cl.Nodegroups, func(ng *fleettest.Nodegroup) string { return ng.Name }))
	})
}

func eksDescribeNodegroup(c *call) (any, error) {
	return c.inCluster(func(s *fleettest.State, cl *fleettest.Cluster) (any, error) {
		name := c.in.str("nodegroupName")
		ng := cl.Nodegroup(name)
		if ng == nil {
			return nil, eksError(&fleettest.NotFoundError{Kind: "nodegroup", Name: name})
		}
		out := object{
			"nodegroupName":  ng.Name,
			"nodegroupArn":   "arn:aws:eks:" + c.region + ":" + s.Account + ":nodegroup/" + cl.Name + "/" + ng.Name + "/fake",
			"clusterName":    cl.Name,
			"version":        ng.Version,
			"releaseVersion": ng.ReleaseVersion,
			"status":         ng.Status,
			"amiType":        ng.AMIType,
			"capacityType":   ng.CapacityType,
			"instanceTypes":  ng.InstanceTypes,
			"subnets":        append([]string{}, ng.Subnets...),
			"scalingConfig":  object{"minSize": ng.Min, "maxSize": ng.Max, "desiredSize": ng.Desired},
			"health":         object{"issues": issuesJSON(ng.HealthIssues)},
			"createdAt":      epoch(ng.CreatedAt),
			"modifiedAt":     epoch(ng.ModifiedAt),
		}
		if len(ng.AutoScalingGroups) > 0 {
			groups := make([]object, 0, len(ng.AutoScalingGroups))
			for _, g := range ng.AutoScalingGroups {
				groups = append(groups, object{"name": g})
			}
			out["resources"] = object{"autoScalingGroups": groups}
		}
		if lt := ng.LaunchTemplate; lt != nil {
			out["launchTemplate"] = object{"id": lt.ID, "version": lt.Version}
		}
		if len(ng.Labels) > 0 {
			out["labels"] = ng.Labels
		}
		return object{"nodegroup": out}, nil
	})
}

// The four Update handlers model the fields refresh sends and no others. A
// request with any other field fails, and the error names the field, so a
// test that sends something new points at the fake and not at the product.
// COVERAGE.md lists the modeled fields.

// onlyFields returns an error if in has a key that is not in modeled.
// where names the object for the message: an operation, or a nested member.
func onlyFields(in input, where string, modeled ...string) error {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if !slices.Contains(modeled, key) {
			return newError("InvalidParameterException", "awsxtest does not model field "+key+" of "+where)
		}
	}
	return nil
}

// optional reads an optional member, keeping whether the request had it.
func optional[T any](in input, key string) *T {
	v, ok := in[key].(T)
	if !ok {
		return nil
	}
	return &v
}

// optionalInt32 reads an optional integer member.
func optionalInt32(in input, key string) *int32 {
	n, ok := in.num(key)
	if !ok {
		return nil
	}
	v := int32(n) //nolint:gosec // a size or a timeout from a test fits int32
	return &v
}

// member returns a nested object member, and whether the request had it.
func member(in input, key string) (input, bool) {
	m, ok := in[key].(map[string]any)
	return m, ok
}

// The modeled fields of each Update operation, path parameters included.
var (
	updateNodegroupVersionFields = []string{"clusterName", "nodegroupName", "version", "releaseVersion", "force", "clientRequestToken"}
	updateNodegroupConfigFields  = []string{"clusterName", "nodegroupName", "scalingConfig", "clientRequestToken"}
	scalingConfigFields          = []string{"desiredSize", "minSize", "maxSize"}
	updateAddonFields            = []string{"clusterName", "addonName", "addonVersion", "configurationValues", "clientRequestToken"}
	updateClusterVersionFields   = []string{"name", "version", "force", "rollbackConfig", "clientRequestToken"}
	rollbackConfigFields         = []string{"timeoutMinutes"}
)

func eksUpdateNodegroupVersion(c *call) (any, error) {
	if err := onlyFields(c.in, "UpdateNodegroupVersion", updateNodegroupVersionFields...); err != nil {
		return nil, err
	}
	u, err := c.world.UpdateNodegroupVersion(c.region, c.clusterName(), c.in.str("nodegroupName"), fleettest.NodegroupVersionRequest{
		Version:        optional[string](c.in, "version"),
		ReleaseVersion: optional[string](c.in, "releaseVersion"),
		Force:          optional[bool](c.in, "force"),
		Token:          c.in.str("clientRequestToken"),
	})
	return updateResult(u, err)
}

func eksUpdateNodegroupConfig(c *call) (any, error) {
	if err := onlyFields(c.in, "UpdateNodegroupConfig", updateNodegroupConfigFields...); err != nil {
		return nil, err
	}
	req := fleettest.NodegroupConfigRequest{Token: c.in.str("clientRequestToken")}
	if scaling, ok := member(c.in, "scalingConfig"); ok {
		if err := onlyFields(scaling, "UpdateNodegroupConfig scalingConfig", scalingConfigFields...); err != nil {
			return nil, err
		}
		req.Scaling = &fleettest.ScalingRequest{
			Desired: optionalInt32(scaling, "desiredSize"),
			Min:     optionalInt32(scaling, "minSize"),
			Max:     optionalInt32(scaling, "maxSize"),
		}
	}
	u, err := c.world.UpdateNodegroupConfig(c.region, c.clusterName(), c.in.str("nodegroupName"), req)
	return updateResult(u, err)
}

func eksListAddons(c *call) (any, error) {
	return c.inCluster(func(_ *fleettest.State, cl *fleettest.Cluster) (any, error) {
		return paged(c, "addons", names(cl.Addons, func(a *fleettest.Addon) string { return a.Name }))
	})
}

func eksDescribeAddon(c *call) (any, error) {
	return c.inCluster(func(s *fleettest.State, cl *fleettest.Cluster) (any, error) {
		name := c.in.str("addonName")
		a := cl.Addon(name)
		if a == nil {
			return nil, eksError(&fleettest.NotFoundError{Kind: "addon", Name: name})
		}
		out := object{
			"addonName":    a.Name,
			"addonVersion": a.Version,
			"addonArn":     "arn:aws:eks:" + c.region + ":" + s.Account + ":addon/" + cl.Name + "/" + a.Name + "/fake",
			"clusterName":  cl.Name,
			"status":       a.Status,
			"health":       object{"issues": issuesJSON(a.HealthIssues)},
			"createdAt":    epoch(a.CreatedAt),
			"modifiedAt":   epoch(a.ModifiedAt),
		}
		if a.ServiceAccountRoleARN != "" {
			out["serviceAccountRoleArn"] = a.ServiceAccountRoleARN
		}
		if a.ConfigurationValues != "" {
			out["configurationValues"] = a.ConfigurationValues
		}
		return object{"addon": out}, nil
	})
}

func eksUpdateAddon(c *call) (any, error) {
	if err := onlyFields(c.in, "UpdateAddon", updateAddonFields...); err != nil {
		return nil, err
	}
	u, err := c.world.UpdateAddon(c.region, c.clusterName(), c.in.str("addonName"), fleettest.AddonRequest{
		Version:             optional[string](c.in, "addonVersion"),
		ConfigurationValues: optional[string](c.in, "configurationValues"),
		Token:               c.in.str("clientRequestToken"),
	})
	return updateResult(u, err)
}

func eksUpdateClusterVersion(c *call) (any, error) {
	if err := onlyFields(c.in, "UpdateClusterVersion", updateClusterVersionFields...); err != nil {
		return nil, err
	}
	req := fleettest.ClusterVersionRequest{
		Version: optional[string](c.in, "version"),
		Force:   optional[bool](c.in, "force"),
		Token:   c.in.str("clientRequestToken"),
	}
	if rollback, ok := member(c.in, "rollbackConfig"); ok {
		if err := onlyFields(rollback, "UpdateClusterVersion rollbackConfig", rollbackConfigFields...); err != nil {
			return nil, err
		}
		req.Rollback = &fleettest.RollbackRequest{TimeoutMinutes: optionalInt32(rollback, "timeoutMinutes")}
	}
	u, err := c.world.UpdateClusterVersion(c.region, c.clusterName(), req)
	return updateResult(u, err)
}

func updateResult(u fleettest.Update, err error) (any, error) {
	if err != nil {
		return nil, eksError(err)
	}
	return object{"update": updateJSON(&u)}, nil
}

func updateJSON(u *fleettest.Update) object {
	params := make([]object, 0, len(u.Params))
	for _, p := range u.Params {
		params = append(params, object{"type": p.Type, "value": p.Value})
	}
	errs := make([]object, 0, len(u.Errors))
	for _, e := range u.Errors {
		errs = append(errs, object{"errorCode": e.Code, "errorMessage": e.Message, "resourceIds": append([]string{}, e.ResourceIDs...)})
	}
	return object{
		"id":        u.ID,
		"type":      u.Type,
		"status":    string(u.Status),
		"params":    params,
		"createdAt": epoch(u.CreatedAt),
		"errors":    errs,
	}
}

// eksListUpdates lists the updates of the nodegroup or the add-on the
// request names, or those of the cluster itself when it names neither.
func eksListUpdates(c *call) (any, error) {
	return c.inCluster(func(_ *fleettest.State, cl *fleettest.Cluster) (any, error) {
		var ids []string
		for _, u := range cl.Updates {
			if u.Nodegroup == c.in.str("nodegroupName") && u.Addon == c.in.str("addonName") {
				ids = append(ids, u.ID)
			}
		}
		return paged(c, "updateIds", ids)
	})
}

// eksDescribeUpdate finds an update only under the resource it belongs to:
// EKS needs nodegroupName or addonName for an update of one.
func eksDescribeUpdate(c *call) (any, error) {
	return c.inCluster(func(_ *fleettest.State, cl *fleettest.Cluster) (any, error) {
		id := c.in.str("updateId")
		u := cl.Update(id)
		if u == nil || u.Nodegroup != c.in.str("nodegroupName") || u.Addon != c.in.str("addonName") {
			return nil, eksNotFound("No update found for ID: " + id)
		}
		return object{"update": updateJSON(u)}, nil
	})
}

func eksListInsights(c *call) (any, error) {
	filter := c.in.obj("filter")
	keep := func(key, value string) bool {
		want := filter.strs(key)
		return len(want) == 0 || slices.Contains(want, value)
	}
	return c.inCluster(func(_ *fleettest.State, cl *fleettest.Cluster) (any, error) {
		var list []object
		for i := range cl.Insights {
			in := &cl.Insights[i]
			version := cmp.Or(in.KubernetesVersion, cl.NextMinor())
			if keep("categories", in.Category) && keep("statuses", string(in.Status)) && keep("kubernetesVersions", version) {
				list = append(list, insightJSON(cl, in))
			}
		}
		return paged(c, "insights", list)
	})
}

func eksDescribeInsight(c *call) (any, error) {
	return c.inCluster(func(_ *fleettest.State, cl *fleettest.Cluster) (any, error) {
		id := c.in.str("id")
		for i := range cl.Insights {
			in := &cl.Insights[i]
			if in.ID != id {
				continue
			}
			out := insightJSON(cl, in)
			out["recommendation"] = in.Recommendation
			if len(in.AdditionalInfo) > 0 {
				out["additionalInfo"] = in.AdditionalInfo
			}
			resources := make([]object, 0, len(in.Resources))
			for _, r := range in.Resources {
				resources = append(resources, object{
					"kubernetesResourceUri": r.URI,
					"arn":                   r.ARN,
					"insightStatus":         object{"status": r.Status, "reason": r.Reason},
				})
			}
			out["resources"] = resources
			if len(in.Deprecations) > 0 {
				out["categorySpecificSummary"] = object{"deprecationDetails": deprecationsJSON(in.Deprecations)}
			}
			return object{"insight": out}, nil
		}
		return nil, eksNotFound("No insight found for id: " + id + ".")
	})
}

func insightJSON(cl *fleettest.Cluster, in *fleettest.Insight) object {
	return object{
		"id":                in.ID,
		"name":              in.Name,
		"category":          in.Category,
		"kubernetesVersion": cmp.Or(in.KubernetesVersion, cl.NextMinor()),
		"description":       in.Description,
		"insightStatus":     object{"status": in.Status, "reason": in.Reason},
	}
}

func deprecationsJSON(deprecations []fleettest.Deprecation) []object {
	out := make([]object, 0, len(deprecations))
	for _, d := range deprecations {
		clients := make([]object, 0, len(d.Clients))
		for _, cs := range d.Clients {
			clients = append(clients, object{
				"userAgent":                  cs.UserAgent,
				"numberOfRequestsLast30Days": cs.Requests,
				"lastRequestTime":            epoch(cs.LastRequest),
			})
		}
		out = append(out, object{
			"usage":                          d.Usage,
			"replacedWith":                   d.ReplacedWith,
			"stopServingVersion":             d.StopServingVersion,
			"startServingReplacementVersion": d.StartServingReplacementVersion,
			"clientStats":                    clients,
		})
	}
	return out
}

func eksStartInsightsRefresh(c *call) (any, error) {
	if err := c.world.StartInsightsRefresh(c.region, c.clusterName()); err != nil {
		return nil, eksError(err)
	}
	return object{"status": fleettest.RefreshInProgress, "message": "Insights refresh started"}, nil
}

// eksDescribeInsightsRefresh answers COMPLETED for a cluster that never had
// a refresh started, as it does after one that finished.
func eksDescribeInsightsRefresh(c *call) (any, error) {
	return c.inCluster(func(_ *fleettest.State, cl *fleettest.Cluster) (any, error) {
		r := cl.InsightsRefresh
		if r.Status == "" {
			return object{"status": fleettest.RefreshCompleted}, nil
		}
		return object{"status": r.Status, "startedAt": epoch(r.StartedAt)}, nil
	})
}

// Package fakeaws is an in-process fake of the AWS HTTP APIs refresh calls,
// for tests that run a whole CLI command (flags, AWS setup, action, output)
// without a network or an AWS account.
//
// New starts an httptest server and points the SDK at it through
// AWS_ENDPOINT_URL, with static fake credentials and an isolated config. The
// server answers STS GetCallerIdentity and the EKS REST calls from an
// in-memory world (clusters, nodegroups, addons, updates). Every other call
// (EC2, SSM, Auto Scaling, CloudWatch, Service Quotas, unknown EKS routes)
// gets a non-retryable 400 error, so best-effort lookups degrade the same way
// they do when an AWS call fails.
//
// Tests that use it set process environment variables, so they must not run
// in parallel.
package fakeaws

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Nodegroup is a managed nodegroup in the fake world.
type Nodegroup struct {
	Name    string
	Version string
	// Status defaults to ACTIVE.
	Status string
	// AmiType defaults to AL2_x86_64.
	AmiType string
	// FailUpdate makes UpdateNodegroupVersion fail with InvalidRequestException.
	FailUpdate bool
	// UpdateForce records the force field of the last UpdateNodegroupVersion
	// request (for assertions).
	UpdateForce bool
}

// Addon is an installed EKS addon in the fake world.
type Addon struct {
	Name    string
	Version string
	// Status defaults to ACTIVE.
	Status string
	// Available is the version catalog DescribeAddonVersions returns for
	// this add-on, newest first, all compatible with the cluster version.
	Available []string
	// UpdateStatus is the final DescribeUpdate status of an UpdateAddon
	// update: "" or "Successful" applies the new version; "Failed" or
	// "Cancelled" leaves the add-on as it is.
	UpdateStatus string
	// HealthIssue, when set, is reported as a DescribeAddon health issue.
	HealthIssue string
	// DescribeAddonError, when set, makes DescribeAddon for this add-on fail
	// with that API error code (HTTP 403 for AccessDenied*, else 400).
	DescribeAddonError string
}

// Cluster is an EKS cluster in the fake world.
type Cluster struct {
	Name       string
	Version    string
	Nodegroups []*Nodegroup
	Addons     []*Addon
	// Insights are the cluster's UPGRADE_READINESS insights. Nil answers
	// one PASSING insight for the next minor, as EKS does for a healthy
	// cluster.
	Insights []*Insight
}

// Insight is an EKS Cluster Insight in the fake world.
type Insight struct {
	ID     string
	Name   string
	Status string // PASSING, WARNING, ERROR, or UNKNOWN
}

// Server is the fake AWS endpoint.
type Server struct {
	// URL is the endpoint the SDK is pointed at.
	URL string

	mu       sync.Mutex
	clusters map[string]*Cluster
	updates  map[string]func() // update ID -> mutation applied on first DescribeUpdate
	// updateStatus overrides an update's DescribeUpdate status; the default
	// is Successful. A non-Successful update never applies its mutation.
	updateStatus map[string]string
	nextID       int
	stsError     string
	calls        []string
	// regionError returns the EKS error code to answer for a region, or "".
	regionError func(region string) string
	// hangEKS makes every EKS call block until the client gives up.
	hangEKS bool
}

// FailRegions makes every EKS call signed for a region fail with the API
// error code that code returns for it ("" answers normally). An
// AccessDeniedException models a region closed to these credentials.
func (s *Server) FailRegions(code func(region string) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regionError = code
}

// HangEKS makes every EKS call block until the client cancels it, as with an
// endpoint that accepts connections and never answers.
func (s *Server) HangEKS() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hangEKS = true
}

// New starts a fake AWS endpoint serving clusters and configures the process
// environment for the test so the AWS SDK, the refresh context store, and
// kubeconfig resolution all stay inside the test: no real credentials, no
// refresh context, and no reachable Kubernetes cluster.
func New(tb testing.TB, clusters ...*Cluster) *Server {
	tb.Helper()
	s := &Server{clusters: map[string]*Cluster{}, updates: map[string]func(){}, updateStatus: map[string]string{}}
	for _, c := range clusters {
		s.clusters[c.Name] = c
	}
	ts := httptest.NewServer(http.HandlerFunc(s.serve))
	tb.Cleanup(ts.Close)
	s.URL = ts.URL

	dir := tb.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		tb.Fatal(err)
	}
	for k, v := range map[string]string{
		"AWS_ENDPOINT_URL":            ts.URL,
		"AWS_ACCESS_KEY_ID":           "AKIDFAKEFAKEFAKE",
		"AWS_SECRET_ACCESS_KEY":       "fake-secret",
		"AWS_SESSION_TOKEN":           "",
		"AWS_REGION":                  "us-east-1",
		"AWS_DEFAULT_REGION":          "",
		"AWS_PROFILE":                 "",
		"AWS_CONFIG_FILE":             empty,
		"AWS_SHARED_CREDENTIALS_FILE": empty,
		"AWS_EC2_METADATA_DISABLED":   "true",
		"AWS_MAX_ATTEMPTS":            "1",
		"KUBECONFIG":                  filepath.Join(dir, "no-kubeconfig"),
		"REFRESH_CONFIG_HOME":         dir,
		"REFRESH_CONTEXT":             "",
		"REFRESH_EKS_REGIONS":         "",
		"EKS_CLUSTER_NAME":            "",
		"NO_COLOR":                    "1",
	} {
		tb.Setenv(k, v)
	}
	return s
}

// FailCredentials makes STS GetCallerIdentity fail with the given API error
// code (for example "InvalidClientTokenId"), as with bad credentials.
func (s *Server) FailCredentials(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stsError = code
}

// Calls returns the "service Operation-or-path" of every request served.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// Cluster returns the named cluster's current state (for assertions).
func (s *Server) Cluster(name string) Cluster {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clusters[name]
	if c == nil {
		return Cluster{}
	}
	out := *c
	out.Nodegroups = nil
	for _, ng := range c.Nodegroups {
		cp := *ng
		out.Nodegroups = append(out.Nodegroups, &cp)
	}
	return out
}

// signingService extracts the SigV4 signing name from the Authorization
// header ("Credential=AKID/date/region/<service>/aws4_request").
var signingService = regexp.MustCompile(`Credential=[^/]+/[^/]+/([^/]+)/([^/]+)/aws4_request`)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	service, region := "", ""
	if m := signingService.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		region, service = m[1], m[2]
	}
	body, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	hang := s.hangEKS && service == "eks"
	s.mu.Unlock()
	if hang {
		// Block outside the lock so other calls still get answered.
		<-r.Context().Done()
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, service+" "+r.Method+" "+r.URL.Path)

	switch service {
	case "sts":
		s.serveSTS(w)
	case "eks":
		if s.regionError != nil {
			if code := s.regionError(region); code != "" {
				writeError(w, http.StatusForbidden, code, "fakeaws: region "+region+" answers "+code)
				return
			}
		}
		s.serveEKS(w, r, body)
	default:
		unsupported(w, r, service)
	}
}

func (s *Server) serveSTS(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/xml")
	if s.stsError != "" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>%s</Code><Message>The security token included in the request is invalid.</Message></Error><RequestId>fake</RequestId></ErrorResponse>`, s.stsError)
		return
	}
	_, _ = io.WriteString(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/fake</Arn><UserId>AIDAFAKE</UserId><Account>123456789012</Account></GetCallerIdentityResult><ResponseMetadata><RequestId>fake</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
}

// unsupported answers a call the fake does not model with a non-retryable
// client error in the request's protocol shape.
func unsupported(w http.ResponseWriter, r *http.Request, service string) {
	msg := fmt.Sprintf("fakeaws does not model %s %s %s", service, r.Method, r.URL.Path)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `<Response><Errors><Error><Code>UnsupportedOperation</Code><Message>%s</Message></Error></Errors><RequestID>fake</RequestID></Response>`, html.EscapeString(msg))
		return
	}
	writeError(w, http.StatusBadRequest, "UnsupportedOperation", msg)
}

// writeError writes a JSON-protocol (restJson1 / awsJson) API error.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": code, "message": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serveEKS(w http.ResponseWriter, r *http.Request, body []byte) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	get := r.Method == http.MethodGet

	switch {
	case get && len(parts) == 1 && parts[0] == "clusters":
		names := []string{}
		for name := range s.clusters {
			names = append(names, name)
		}
		writeJSON(w, map[string]any{"clusters": names})
		return
	case get && len(parts) == 1 && parts[0] == "cluster-versions":
		versions := []map[string]any{}
		for _, v := range r.URL.Query()["clusterVersions"] {
			versions = append(versions, map[string]any{"clusterVersion": v, "versionStatus": "STANDARD_SUPPORT"})
		}
		writeJSON(w, map[string]any{"clusterVersions": versions})
		return
	case get && len(parts) == 2 && parts[0] == "addons" && parts[1] == "supported-versions":
		writeJSON(w, map[string]any{"addons": s.addonVersionsJSON(r.URL.Query().Get("addonName"), r.URL.Query().Get("kubernetesVersion"))})
		return
	}

	if len(parts) < 2 || parts[0] != "clusters" {
		unsupported(w, r, "eks")
		return
	}
	c := s.clusters[parts[1]]
	if c == nil {
		writeError(w, http.StatusNotFound, "ResourceNotFoundException", "No cluster found for name: "+parts[1]+".")
		return
	}
	s.serveEKSCluster(w, r, c, parts[2:], body)
}

// serveEKSCluster routes the /clusters/{name}/... calls; rest is the path
// after the cluster name.
func (s *Server) serveEKSCluster(w http.ResponseWriter, r *http.Request, c *Cluster, rest []string, body []byte) {
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	if len(rest) == 0 {
		if get {
			writeJSON(w, map[string]any{"cluster": clusterJSON(c)})
			return
		}
		unsupported(w, r, "eks")
		return
	}

	switch rest[0] {
	case "updates":
		s.serveClusterUpdates(w, r, c, rest[1:], body)
	case "insights":
		if post && len(rest) == 1 {
			writeJSON(w, map[string]any{"insights": insightsJSON(c, body)})
			return
		}
		if get && len(rest) == 2 {
			for _, in := range c.Insights {
				if in.ID == rest[1] {
					writeJSON(w, map[string]any{"insight": insightJSON(c, in)})
					return
				}
			}
			writeError(w, http.StatusNotFound, "ResourceNotFoundException", "No insight found for id: "+rest[1]+".")
			return
		}
		unsupported(w, r, "eks")
	case "insights-refresh":
		// StartInsightsRefresh (POST) and DescribeInsightsRefresh (GET): a
		// refresh completes at once.
		switch {
		case post && len(rest) == 1:
			writeJSON(w, map[string]any{"status": "IN_PROGRESS", "message": "Insights refresh started"})
		case get && len(rest) == 1:
			writeJSON(w, map[string]any{"status": "COMPLETED"})
		default:
			unsupported(w, r, "eks")
		}
	case "node-groups":
		s.serveNodegroups(w, r, c, rest[1:], body)
	case "addons":
		s.serveAddons(w, r, c, rest[1:], body)
	default:
		unsupported(w, r, "eks")
	}
}

// serveClusterUpdates handles UpdateClusterVersion (POST updates) and
// DescribeUpdate (GET updates/{id}).
func (s *Server) serveClusterUpdates(w http.ResponseWriter, r *http.Request, c *Cluster, rest []string, body []byte) {
	switch {
	case r.Method == http.MethodPost && len(rest) == 0:
		var in struct {
			Version string `json:"version"`
		}
		_ = json.Unmarshal(body, &in)
		writeJSON(w, map[string]any{"update": s.startUpdate("VersionUpdate", func() { c.Version = in.Version })})
	case r.Method == http.MethodGet && len(rest) == 1:
		apply, ok := s.updates[rest[0]]
		if !ok {
			writeError(w, http.StatusNotFound, "ResourceNotFoundException", "No update found for ID: "+rest[0])
			return
		}
		status := s.updateStatus[rest[0]]
		if status == "" {
			status = "Successful"
		}
		update := map[string]any{"id": rest[0], "status": status, "type": "VersionUpdate"}
		if status != "Successful" {
			update["errors"] = []any{map[string]any{"errorCode": "AdmissionRequestDenied", "errorMessage": "fake update failure"}}
			writeJSON(w, map[string]any{"update": update})
			return
		}
		if apply != nil {
			apply()
			s.updates[rest[0]] = nil
		}
		writeJSON(w, map[string]any{"update": update})
	default:
		unsupported(w, r, "eks")
	}
}

// serveNodegroups handles ListNodegroups, DescribeNodegroup, and
// UpdateNodegroupVersion.
func (s *Server) serveNodegroups(w http.ResponseWriter, r *http.Request, c *Cluster, rest []string, body []byte) {
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case get && len(rest) == 0:
		names := []string{}
		for _, ng := range c.Nodegroups {
			names = append(names, ng.Name)
		}
		writeJSON(w, map[string]any{"nodegroups": names})
	case get && len(rest) == 1:
		ng := findNodegroup(c, rest[0])
		if ng == nil {
			writeError(w, http.StatusNotFound, "ResourceNotFoundException", "No node group found for name: "+rest[0]+".")
			return
		}
		writeJSON(w, map[string]any{"nodegroup": nodegroupJSON(c, ng)})
	case post && len(rest) == 2 && rest[1] == "update-version":
		ng := findNodegroup(c, rest[0])
		if ng == nil {
			writeError(w, http.StatusNotFound, "ResourceNotFoundException", "No node group found for name: "+rest[0]+".")
			return
		}
		if ng.FailUpdate {
			writeError(w, http.StatusBadRequest, "InvalidRequestException", "fake update failure for "+ng.Name)
			return
		}
		var in struct {
			Version string `json:"version"`
			Force   bool   `json:"force"`
		}
		_ = json.Unmarshal(body, &in)
		ng.UpdateForce = in.Force
		target := in.Version
		if target == "" {
			target = ng.Version
		}
		writeJSON(w, map[string]any{"update": s.startUpdate("VersionUpdate", func() { ng.Version = target })})
	default:
		unsupported(w, r, "eks")
	}
}

// serveAddons handles ListAddons, DescribeAddon, and UpdateAddon.
func (s *Server) serveAddons(w http.ResponseWriter, r *http.Request, c *Cluster, rest []string, body []byte) {
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case get && len(rest) == 0:
		names := []string{}
		for _, a := range c.Addons {
			names = append(names, a.Name)
		}
		writeJSON(w, map[string]any{"addons": names})
	case get && len(rest) == 1:
		a := findAddon(c, rest[0])
		if a == nil {
			writeError(w, http.StatusNotFound, "ResourceNotFoundException", "No addon: "+rest[0])
			return
		}
		if a.DescribeAddonError != "" {
			status := http.StatusBadRequest
			if strings.HasPrefix(a.DescribeAddonError, "AccessDenied") {
				status = http.StatusForbidden
			}
			writeError(w, status, a.DescribeAddonError, "fake DescribeAddon failure for "+a.Name)
			return
		}
		writeJSON(w, map[string]any{"addon": addonJSON(c, a)})
	case post && len(rest) == 2 && rest[1] == "update":
		a := findAddon(c, rest[0])
		if a == nil {
			writeError(w, http.StatusNotFound, "ResourceNotFoundException", "No addon: "+rest[0])
			return
		}
		var in struct {
			AddonVersion string `json:"addonVersion"`
		}
		_ = json.Unmarshal(body, &in)
		target := in.AddonVersion
		if target == "" {
			target = a.Version
		}
		update := s.startUpdate("AddonUpdate", func() { a.Version = target })
		if id, ok := update["id"].(string); ok && a.UpdateStatus != "" {
			s.updateStatus[id] = a.UpdateStatus
		}
		writeJSON(w, map[string]any{"update": update})
	default:
		unsupported(w, r, "eks")
	}
}

func findAddon(c *Cluster, name string) *Addon {
	for _, a := range c.Addons {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func addonJSON(c *Cluster, a *Addon) map[string]any {
	status := a.Status
	if status == "" {
		status = "ACTIVE"
	}
	issues := []any{}
	if a.HealthIssue != "" {
		issues = append(issues, map[string]any{"code": "InsufficientNumberOfReplicas", "message": a.HealthIssue})
	}
	return map[string]any{
		"addonName":    a.Name,
		"addonVersion": a.Version,
		"clusterName":  c.Name,
		"status":       status,
		"health":       map[string]any{"issues": issues},
	}
}

// addonVersionsJSON answers DescribeAddonVersions for addonName from the
// Available catalogs of every cluster's add-on of that name.
func (s *Server) addonVersionsJSON(addonName, k8sVersion string) []any {
	versions := []any{}
	seen := map[string]bool{}
	for _, c := range s.clusters {
		if a := findAddon(c, addonName); a != nil {
			cv := k8sVersion
			if cv == "" {
				cv = c.Version
			}
			for _, v := range a.Available {
				if seen[v] {
					continue
				}
				seen[v] = true
				versions = append(versions, map[string]any{
					"addonVersion":    v,
					"compatibilities": []any{map[string]any{"clusterVersion": cv}},
				})
			}
		}
	}
	if len(versions) == 0 {
		return []any{}
	}
	return []any{map[string]any{"addonName": addonName, "addonVersions": versions}}
}

// startUpdate records an in-progress update whose mutation lands on the first
// DescribeUpdate, and returns its API shape.
func (s *Server) startUpdate(kind string, apply func()) map[string]any {
	s.nextID++
	id := fmt.Sprintf("update-%d", s.nextID)
	s.updates[id] = apply
	return map[string]any{"id": id, "status": "InProgress", "type": kind}
}

func findNodegroup(c *Cluster, name string) *Nodegroup {
	for _, ng := range c.Nodegroups {
		if ng.Name == name {
			return ng
		}
	}
	return nil
}

// insightsJSON answers ListInsights. With c.Insights set it returns them,
// filtered by the request's statuses. Otherwise it answers the way EKS does
// for a healthy cluster: one PASSING upgrade-readiness insight for the next
// minor after the control plane, filtered by the request's
// kubernetesVersions.
func insightsJSON(c *Cluster, body []byte) []any {
	var in struct {
		Filter struct {
			KubernetesVersions []string `json:"kubernetesVersions"`
			Statuses           []string `json:"statuses"`
		} `json:"filter"`
	}
	_ = json.Unmarshal(body, &in)
	if c.Insights != nil {
		out := []any{}
		for _, ins := range c.Insights {
			if len(in.Filter.Statuses) == 0 || slices.Contains(in.Filter.Statuses, ins.Status) {
				out = append(out, insightJSON(c, ins))
			}
		}
		return out
	}
	var major, minor int
	if _, err := fmt.Sscanf(c.Version, "%d.%d", &major, &minor); err != nil {
		return []any{}
	}
	next := fmt.Sprintf("%d.%d", major, minor+1)
	if len(in.Filter.KubernetesVersions) > 0 && !slices.Contains(in.Filter.KubernetesVersions, next) {
		return []any{}
	}
	return []any{map[string]any{
		"id":                "insight-" + c.Name + "-skew",
		"name":              "Kubelet version skew",
		"category":          "UPGRADE_READINESS",
		"kubernetesVersion": next,
		"insightStatus":     map[string]any{"status": "PASSING"},
	}}
}

// insightJSON is one configured insight, for the next minor.
func insightJSON(c *Cluster, in *Insight) map[string]any {
	var major, minor int
	_, _ = fmt.Sscanf(c.Version, "%d.%d", &major, &minor)
	return map[string]any{
		"id":                in.ID,
		"name":              in.Name,
		"category":          "UPGRADE_READINESS",
		"kubernetesVersion": fmt.Sprintf("%d.%d", major, minor+1),
		"insightStatus":     map[string]any{"status": in.Status},
	}
}

func clusterJSON(c *Cluster) map[string]any {
	return map[string]any{
		"name":            c.Name,
		"arn":             "arn:aws:eks:us-east-1:123456789012:cluster/" + c.Name,
		"version":         c.Version,
		"status":          "ACTIVE",
		"endpoint":        "https://" + c.Name + ".eks.fake.invalid",
		"platformVersion": "eks.1",
	}
}

func nodegroupJSON(c *Cluster, ng *Nodegroup) map[string]any {
	status, amiType := ng.Status, ng.AmiType
	if status == "" {
		status = "ACTIVE"
	}
	if amiType == "" {
		amiType = "AL2_x86_64"
	}
	return map[string]any{
		"nodegroupName":  ng.Name,
		"clusterName":    c.Name,
		"version":        ng.Version,
		"releaseVersion": ng.Version + ".0-20260101",
		"status":         status,
		"amiType":        amiType,
		"capacityType":   "ON_DEMAND",
		"instanceTypes":  []string{"m5.large"},
		"scalingConfig":  map[string]any{"minSize": 1, "maxSize": 3, "desiredSize": 2},
		"health":         map[string]any{"issues": []any{}},
	}
}

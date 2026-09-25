// Package live is the TUI's backend for real AWS accounts. It keeps a
// fleet picture from a background multi-region sweep (the same status
// service behind `refresh status`), runs `cluster upgrade-check` for the
// readiness screen, and dry-runs changes with the real planners.
//
// It is read-only unless Options.AllowChanges is set. With it, a nodegroup
// roll can start: the pre-flight health gate runs again, the roll starts
// through nodegroup.StartNodegroupRoll, and it is watched through the EKS
// update (the authority on the result) and, when a kubeconfig context
// reaches the cluster, the noderoll observer. Add-on updates and upgrades
// stay dry runs that name the CLI command. State is served from memory, so
// the TUI's fast polling never turns into AWS calls.
package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/regionsweep"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// ErrReadOnly is returned by every call that would change a cluster.
var ErrReadOnly = errors.New("the UI is read-only: restart it with --allow-changes, or copy the CLI command (c) and run it in a shell")

// Event list bounds, as in the simulator.
const (
	feedCap = 500
	logCap  = 400
)

// Options configures a Backend.
type Options struct {
	// Regions to sweep. Empty means the base config's region.
	Regions []string
	// SkipInaccessible skips regions closed to the credentials (a default
	// partition sweep) instead of reporting them as failures.
	SkipInaccessible bool
	// Interval between fleet sweeps. Zero means one minute.
	Interval time.Duration
	// SweepTimeout bounds one sweep. Zero means two minutes.
	SweepTimeout time.Duration
	// MaxConcurrency caps the clusters evaluated at once per region.
	MaxConcurrency int
	// Context and Profile label the top bar.
	Context, Profile string
	// Logger receives the services' logs. Nil sends warnings and errors to
	// the TUI's log pane: the TUI owns the terminal, so nothing may write
	// to stderr while it runs.
	Logger *slog.Logger
	// AllowChanges lets Start begin a nodegroup roll. Off, every change is
	// a dry run.
	AllowChanges bool
	// Kubeconfig and KubeContext pick the node view's Kubernetes access, as
	// --kubeconfig and --kube-context do for `nodegroup update`.
	Kubeconfig, KubeContext string
	// WaitTimeout bounds how long a roll is watched (0 = no limit).
	WaitTimeout time.Duration
	// ObserveInterval is how often a roll's nodes are read. Zero means two
	// seconds.
	ObserveInterval time.Duration
	// PollInterval is how often a roll's EKS update is read. Zero means the
	// CLI's default.
	PollInterval time.Duration
	// CallTimeout bounds a single call that changes a cluster. Zero means
	// one minute.
	CallTimeout time.Duration
}

// services are the AWS-facing calls. Tests replace them.
type services struct {
	listStatuses  func(ctx context.Context, cfg aws.Config, opts statussvc.ListOptions) ([]statussvc.ClusterStatus, error)
	latestVersion func(ctx context.Context, cfg aws.Config) (string, error)
	upgradeCheck  func(ctx context.Context, cfg aws.Config, cluster string) (*clustersvc.UpgradeReport, error)
	buildPlan     func(ctx context.Context, cfg aws.Config, cluster, target string) (*upgrade.Plan, error)
}

// target is where a fleet row lives. Keys are unique even when two regions
// hold clusters with the same name.
type target struct {
	name, region string
}

// Backend is the live state.Backend.
type Backend struct {
	base aws.Config
	opts Options
	svc  services
	roll rollServices
	now  func() time.Time

	// wake asks the sweep loop to sweep now.
	wake chan struct{}
	// work tracks readiness runs, so Close can wait for them.
	work sync.WaitGroup

	mu        sync.Mutex
	runCtx    context.Context
	clusters  []state.Cluster
	targets   map[string]target
	feed, log []state.Event
	seq       uint64
	readiness map[string]*state.Readiness
	syncedAt  time.Time
	answered  int
	total     int
	sweeping  bool
	// prev is the last sweep's rows by region and name, for change events.
	prev map[target]state.Cluster
	// warnedClosed is set once the feed said no region is accessible.
	warnedClosed bool
	// rolls are the rolls this backend started, oldest first.
	rolls []*liveRoll
	// claimed names the change this backend runs on a cluster.
	claimed map[target]string
	// accepted holds the health findings each roll's dry run showed, by
	// acceptKey: the findings the user confirmed with y.
	accepted map[string][]string
}

// New returns a backend for the accounts behind cfg. Call Run to start
// sweeping.
func New(cfg aws.Config, opts Options) *Backend {
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.SweepTimeout <= 0 {
		opts.SweepTimeout = 2 * time.Minute
	}
	if opts.ObserveInterval <= 0 {
		opts.ObserveInterval = 2 * time.Second
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = appconfig.DefaultPollInterval
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = time.Minute
	}
	if len(opts.Regions) == 0 && cfg.Region != "" {
		opts.Regions = []string{cfg.Region}
	}
	b := &Backend{
		base:      cfg,
		opts:      opts,
		now:       time.Now,
		wake:      make(chan struct{}, 1),
		targets:   map[string]target{},
		readiness: map[string]*state.Readiness{},
		claimed:   map[target]string{},
		accepted:  map[string][]string{},
	}
	if b.opts.Logger == nil {
		b.opts.Logger = slog.New(&paneHandler{b: b, level: slog.LevelWarn})
	}
	b.svc = defaultServices(b.opts.Logger)
	b.roll = defaultRollServices(b.opts)
	return b
}

// paneHandler writes log records at or above level into the log pane.
type paneHandler struct {
	b     *Backend
	level slog.Level
	attrs []slog.Attr
}

func (h *paneHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *paneHandler) Handle(_ context.Context, r slog.Record) error {
	text := r.Message
	add := func(a slog.Attr) bool {
		text += " " + a.Key + "=" + a.Value.String()
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	lvl := state.LevelWarn
	if r.Level >= slog.LevelError {
		lvl = state.LevelError
	}
	h.b.mu.Lock()
	defer h.b.mu.Unlock()
	h.b.api("log", text, lvl)
	return nil
}

func (h *paneHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &paneHandler{b: h.b, level: h.level, attrs: append(slices.Clone(h.attrs), as...)}
}

// WithGroup keeps the handler flat: the pane has one line per record.
func (h *paneHandler) WithGroup(string) slog.Handler { return h }

func defaultServices(logger *slog.Logger) services {
	return services{
		listStatuses: func(ctx context.Context, cfg aws.Config, opts statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
			return statussvc.NewService(cfg, logger).ListClusterStatuses(ctx, opts)
		},
		latestVersion: func(ctx context.Context, cfg aws.Config) (string, error) {
			client := factory.NewEKSClient(cfg)
			versions, err := awsinternal.ListAllPages(ctx, "listing EKS versions",
				func(rc context.Context, token *string) (*eks.DescribeClusterVersionsOutput, error) {
					return client.DescribeClusterVersions(rc, &eks.DescribeClusterVersionsInput{NextToken: token})
				},
				func(out *eks.DescribeClusterVersionsOutput) ([]string, *string) {
					var vs []string
					for _, v := range out.ClusterVersions {
						vs = append(vs, aws.ToString(v.ClusterVersion))
					}
					return vs, out.NextToken
				})
			if err != nil {
				return "", err
			}
			return newest(versions), nil
		},
		upgradeCheck: func(ctx context.Context, cfg aws.Config, cluster string) (*clustersvc.UpgradeReport, error) {
			report, err := factory.NewClusterService(cfg, false, logger).UpgradeCheck(ctx, cluster, clustersvc.UpgradeCheckOptions{ShowPassing: true})
			if err != nil || report == nil {
				return report, err
			}
			// What `cluster upgrade-check` adds after the service call: the
			// support posture (with the cluster's upgrade policy) and the
			// control-plane health gate.
			if report.Skew.ControlPlaneVersion != "" {
				posture := statussvc.NewSupportResolver(factory.NewEKSClient(cfg)).Resolve(ctx, report.Skew.ControlPlaneVersion)
				posture = statussvc.ApplySupportType(posture, ekstypes.SupportType(report.SupportType))
				report.Support = &posture
			}
			cp := health.NewCheckerForConfig(cfg, nil, nil).CheckControlPlaneMetrics(ctx, cluster)
			report.ControlPlane = &cp
			return report, nil
		},
		buildPlan: func(ctx context.Context, cfg aws.Config, cluster, target string) (*upgrade.Plan, error) {
			svc := upgrade.NewService(factory.NewEKSClient(cfg), logger)
			return svc.BuildPlan(ctx, cluster, target, upgrade.PlanOptions{Preview: true})
		},
	}
}

// newest returns the highest "1.NN" version in vs.
func newest(vs []string) string {
	best := ""
	for _, v := range vs {
		if state.Minor(v) > state.Minor(best) {
			best = v
		}
	}
	return best
}

// Run sweeps the fleet now and every Interval until ctx ends.
func (b *Backend) Run(ctx context.Context) {
	t := time.NewTicker(b.opts.Interval)
	defer t.Stop()
	b.run(ctx, t.C)
}

func (b *Backend) run(ctx context.Context, ticks <-chan time.Time) {
	b.mu.Lock()
	b.runCtx = ctx
	b.mu.Unlock()
	for {
		b.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		case <-b.wake:
		}
	}
}

// Close waits for readiness runs started by the backend to finish. Cancel
// the context passed to Run first.
func (b *Backend) Close() { b.work.Wait() }

// Refresh asks for a sweep now.
func (b *Backend) Refresh() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// stamp gives an event the next sequence number and the current time. The
// caller holds b.mu.
func (b *Backend) stamp(e state.Event) state.Event {
	b.seq++
	e.Seq = b.seq
	if e.At.IsZero() {
		e.At = b.now()
	}
	return e
}

func (b *Backend) emit(e state.Event) { b.feed = appendCapped(b.feed, b.stamp(e), feedCap) }

// api records a fleet-level AWS call in the log.
func (b *Backend) api(op, text string, lvl state.Level) {
	b.log = appendCapped(b.log, b.stamp(state.Event{Source: state.SourceAWS, Level: lvl, Subject: op, Text: text}), logCap)
}

func appendCapped(list []state.Event, e state.Event, max int) []state.Event {
	list = append(list, e)
	if over := len(list) - max; over > 0 {
		n := copy(list, list[over:])
		list = list[:n]
	}
	return list
}

// sweep reads the fleet across the configured regions.
func (b *Backend) sweep(ctx context.Context) {
	b.mu.Lock()
	b.sweeping = true
	b.api("sweep", fmt.Sprintf("status sweep of %d region(s)", len(b.opts.Regions)), state.LevelProgress)
	b.mu.Unlock()
	start := b.now()

	sctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
	defer cancel()
	// The base config has no region in a default partition sweep; ask in
	// the first region of the sweep.
	vcfg := b.base.Copy()
	if len(b.opts.Regions) > 0 {
		vcfg.Region = b.opts.Regions[0]
	}
	latest, lerr := b.svc.latestVersion(sctx, vcfg)
	res := regionsweep.Run(sctx, b.opts.Regions, regionsweep.Options{Concurrency: regionConcurrency(b.opts.MaxConcurrency), SkipInaccessible: b.opts.SkipInaccessible},
		func(rctx context.Context, region string) ([]statussvc.ClusterStatus, error) {
			cfg := b.base.Copy()
			cfg.Region = region
			rows, err := b.svc.listStatuses(rctx, cfg, statussvc.ListOptions{MaxConcurrency: b.opts.MaxConcurrency, Detail: true})
			if err != nil && len(rows) > 0 {
				return rows, nil // listed, but stopped early: the rows say so
			}
			return rows, err
		})

	var rows []statussvc.ClusterStatus
	for _, a := range res.Answered {
		rows = append(rows, a.Value...)
	}
	clusters, targets := toClusters(rows, latest)

	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.Err() != nil {
		b.sweeping = false
		return // shutting down: keep the last good picture
	}
	took := b.now().Sub(start).Round(100 * time.Millisecond)
	if lerr != nil {
		b.api("DescribeClusterVersions", "newest EKS version unknown: "+lerr.Error(), state.LevelWarn)
	}
	for _, a := range res.Answered {
		b.api("status", fmt.Sprintf("%s · %d cluster(s)", a.Region, len(a.Value)), state.LevelInfo)
	}
	for i, f := range res.Failed {
		b.api("status", f.Name+" failed: "+res.Errors[i].Error(), state.LevelError)
		b.emit(state.Event{Source: state.SourceAWS, Level: state.LevelError, Subject: f.Name, Text: "region could not be read", Detail: res.Errors[i].Error()})
	}
	if n := len(res.Skipped); n > 0 {
		why := ""
		if err := res.SkipErrors[res.Skipped[0]]; err != nil {
			why = " · " + res.Skipped[0] + ": " + err.Error()
		}
		b.api("status", fmt.Sprintf("skipped %d region(s) closed to these credentials%s", n, why), state.LevelWarn)
		if len(res.Answered) == 0 && len(res.Failed) == 0 && !b.warnedClosed {
			// Every region was closed: an empty fleet here is not a clean one.
			b.warnedClosed = true
			b.emit(state.Event{Source: state.SourceAWS, Level: state.LevelError, Subject: "credentials",
				Text: "no region is accessible to these credentials", Detail: "check eks:ListClusters, or scope with -r or REFRESH_EKS_REGIONS" + why})
		}
	}
	b.diff(clusters, targets)
	b.clusters, b.targets = clusters, targets
	b.answered, b.total = len(res.Answered), len(b.opts.Regions)-len(res.Skipped)
	b.syncedAt = b.now()
	b.sweeping = false
	b.api("sweep", fmt.Sprintf("%d cluster(s) in %d region(s) · %s", len(clusters), len(res.Answered), took), state.LevelOK)
}

// regionConcurrency matches `refresh status`: four regions at a time, fewer
// when --max-concurrency asks for fewer.
func regionConcurrency(maxConcurrency int) int {
	const regions = 4
	if maxConcurrency > 0 && maxConcurrency < regions {
		return maxConcurrency
	}
	return regions
}

// diff turns what changed since the last sweep into feed events. The first
// sweep reports every cluster that is not current. The caller holds b.mu.
func (b *Backend) diff(now []state.Cluster, targets map[string]target) {
	first := b.prev == nil
	// Keyed by region and name: a display key can change (a second region
	// gains a cluster of the same name) while the cluster stays the same.
	next := make(map[target]state.Cluster, len(now))
	for _, c := range now {
		id := targets[c.Name]
		next[id] = c
		old, seen := b.prev[id]
		switch {
		case first:
			if lvl, text := c.Health(); lvl != state.LevelOK {
				b.emit(state.Event{Cluster: c.Name, Source: state.SourceAWS, Level: lvl, Subject: c.Region, Text: text})
			}
		case !seen:
			b.emit(state.Event{Cluster: c.Name, Source: state.SourceAWS, Level: state.LevelInfo, Subject: c.Region, Text: "cluster found", Detail: c.Version})
		default:
			if old.Version != c.Version {
				b.emit(state.Event{Cluster: c.Name, Source: state.SourceAWS, Level: state.LevelOK, Subject: "control plane", Text: old.Version + " → " + c.Version})
			}
			for _, ng := range c.Nodegroups {
				for _, o := range old.Nodegroups {
					if o.Name == ng.Name && o.Status != ng.Status {
						b.emit(state.Event{Cluster: c.Name, Source: state.SourceAWS, Level: statusLevel(ng.Status), Subject: ng.Name, Text: o.Status + " → " + ng.Status})
					}
				}
			}
			if ol, _ := old.Health(); ol != state.LevelOK {
				if nl, text := c.Health(); nl == state.LevelOK {
					b.emit(state.Event{Cluster: c.Name, Source: state.SourceAWS, Level: state.LevelOK, Subject: c.Region, Text: text})
				}
			}
		}
	}
	for id, old := range b.prev {
		if _, ok := next[id]; !ok {
			b.emit(state.Event{Cluster: old.Name, Source: state.SourceAWS, Level: state.LevelWarn, Subject: id.region, Text: "cluster no longer listed"})
		}
	}
	b.prev = next
}

func statusLevel(s string) state.Level {
	switch s {
	case "ACTIVE":
		return state.LevelOK
	case "UPDATING", "CREATING":
		return state.LevelProgress
	case "DEGRADED", "CREATE_FAILED", "DELETE_FAILED":
		return state.LevelError
	default:
		return state.LevelInfo
	}
}

// State implements state.Backend. It never calls AWS.
func (b *Backend) State(ctx context.Context) (state.State, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return state.State{}, err
	}
	st := state.State{
		Now:             b.now(),
		Seq:             b.seq,
		Backend:         "live",
		Badge:           "READ-ONLY",
		Context:         b.opts.Context,
		Profile:         b.opts.Profile,
		RegionsAnswered: b.answered,
		RegionsTotal:    b.total,
		SyncedAt:        b.syncedAt,
		Feed:            slices.Clone(b.feed),
		Log:             slices.Clone(b.log),
		Readiness:       make(map[string]state.Readiness, len(b.readiness)),
	}
	if b.opts.AllowChanges {
		st.Badge = "CHANGES ON"
	}
	for _, c := range b.clusters {
		c.Nodegroups = slices.Clone(c.Nodegroups)
		c.Addons = slices.Clone(c.Addons)
		c.Busy = b.busyOf(c.Name, c)
		st.Clusters = append(st.Clusters, c)
	}
	for _, r := range b.rolls {
		st.Rolls = append(st.Rolls, b.rollSnapshot(r))
	}
	for k, r := range b.readiness {
		st.Readiness[k] = copyReadiness(*r)
	}
	return st, nil
}

func copyReadiness(r state.Readiness) state.Readiness {
	checks := make([]state.Check, len(r.Checks))
	for i, c := range r.Checks {
		c.Detail = slices.Clone(c.Detail)
		c.Fix = slices.Clone(c.Fix)
		if c.Table != nil {
			t := state.Table{Header: slices.Clone(c.Table.Header)}
			for _, row := range c.Table.Rows {
				t.Rows = append(t.Rows, slices.Clone(row))
			}
			c.Table = &t
		}
		checks[i] = c
	}
	r.Checks = checks
	r.Log = slices.Clone(r.Log)
	return r
}

// cfgFor returns the AWS config and real name for a fleet key.
func (b *Backend) cfgFor(key string) (aws.Config, target, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.targets[key]
	if !ok {
		return aws.Config{}, target{}, fmt.Errorf("cluster %q is not in the fleet", key)
	}
	cfg := b.base.Copy()
	cfg.Region = t.region
	return cfg, t, nil
}

func (b *Backend) cluster(key string) (state.Cluster, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.clusters {
		if c.Name == key {
			return c, true
		}
	}
	return state.Cluster{}, false
}

// RunReadiness implements state.Backend: it starts `cluster upgrade-check`
// in the background and returns at once.
func (b *Backend) RunReadiness(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, t, err := b.cfgFor(key)
	if err != nil {
		return err
	}
	c, _ := b.cluster(key)
	b.mu.Lock()
	if r := b.readiness[key]; r != nil && r.Running {
		b.mu.Unlock()
		return nil
	}
	run := &state.Readiness{Cluster: key, From: c.Version, To: nextHop(c), StartedAt: b.now(), Running: true,
		Checks: []state.Check{{Group: "UPGRADE CHECK", Name: "cluster upgrade-check", Status: state.CheckRunning}}}
	run.Log = appendCapped(run.Log, b.stamp(state.Event{Cluster: key, Source: state.SourceCheck, Level: state.LevelProgress, Subject: "UpgradeCheck", Text: "insights, version skew, control plane health"}), logCap)
	b.readiness[key] = run
	runCtx := b.runCtx
	b.mu.Unlock()
	if runCtx == nil {
		runCtx = ctx
	}

	b.work.Add(1)
	go func() {
		defer b.work.Done()
		start := b.now()
		cctx, cancel := context.WithTimeout(runCtx, b.opts.SweepTimeout)
		defer cancel()
		report, rerr := b.svc.upgradeCheck(cctx, cfg, t.name)
		if rerr == nil && report == nil {
			rerr = errors.New("cluster upgrade-check returned no report")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.readiness[key] != run {
			return // a newer run replaced this one
		}
		run.Running = false
		took := b.now().Sub(start).Round(100 * time.Millisecond)
		if rerr != nil {
			run.Checks = []state.Check{{Group: "UPGRADE CHECK", Name: "cluster upgrade-check", Status: state.CheckFail,
				Summary: "could not run", Detail: []string{rerr.Error()}, Source: "refresh cluster upgrade-check"}}
			run.Log = appendCapped(run.Log, b.stamp(state.Event{Cluster: key, Source: state.SourceCheck, Level: state.LevelError, Subject: "UpgradeCheck", Text: rerr.Error()}), logCap)
			return
		}
		run.Checks = readinessChecks(report, run.To, regionFlag(t))
		run.Log = appendCapped(run.Log, b.stamp(state.Event{Cluster: key, Source: state.SourceCheck, Level: state.LevelOK, Subject: "UpgradeCheck",
			Text: fmt.Sprintf("%d insight(s) · %d nodegroup(s) · %d add-on(s) · %s", len(report.Insights), len(report.Skew.Nodegroups), len(report.Skew.Addons), took)}), logCap)
		lvl, text := state.LevelOK, "ready to upgrade"
		switch {
		case run.Blockers() > 0:
			lvl, text = state.LevelError, fmt.Sprintf("readiness: %d blocker(s)", run.Blockers())
		case run.Warnings() > 0:
			lvl, text = state.LevelWarn, fmt.Sprintf("readiness: %d warning(s)", run.Warnings())
		}
		b.emit(state.Event{Cluster: key, Source: state.SourceCheck, Level: lvl, Subject: run.From + " → " + run.To, Text: text})
	}()
	return nil
}

// nextHop is the version an upgrade of c would move to. With the newest
// version unknown it is the next minor; the planner checks that it exists.
func nextHop(c state.Cluster) string {
	if c.Latest != "" && c.Version == c.Latest {
		return c.Version
	}
	return state.NextMinor(c.Version)
}

// Plan implements state.Backend. Roll and add-on plans come from the last
// sweep; upgrade plans call the real planner in preview mode.
func (b *Backend) Plan(ctx context.Context, a state.Action) (state.Plan, error) {
	if err := ctx.Err(); err != nil {
		return state.Plan{}, err
	}
	c, ok := b.cluster(a.Cluster)
	if !ok {
		return state.Plan{}, fmt.Errorf("cluster %q is not in the fleet", a.Cluster)
	}
	cfg, t, err := b.cfgFor(a.Cluster)
	if err != nil {
		return state.Plan{}, err
	}
	var p state.Plan
	switch a.Kind {
	case state.ActionRoll:
		p, err = planRoll(c, t, a.Nodegroup)
		if err == nil && b.opts.AllowChanges {
			pctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
			b.planRollLive(pctx, &p, cfg, t, a.Nodegroup)
			cancel()
		}
	case state.ActionAddons:
		p = planAddons(c, t)
	case state.ActionUpgrade:
		to := nextHop(c)
		if to == c.Version {
			p = state.Plan{Action: a, Title: "Upgrade cluster · " + a.Cluster, Blocked: a.Cluster + " already runs " + c.Version + ", the newest version"}
			break
		}
		pctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
		plan, perr := b.svc.buildPlan(pctx, cfg, t.name, to)
		cancel()
		if perr != nil {
			return state.Plan{}, perr
		}
		p = planUpgrade(c, t, plan)
	default:
		return state.Plan{}, fmt.Errorf("unknown action %d", a.Kind)
	}
	if err != nil {
		return state.Plan{}, err
	}
	p.Action = a
	b.mu.Lock()
	busy := b.busyOf(a.Cluster, c)
	b.mu.Unlock()
	if p.Blocked == "" && busy != "" {
		p.Blocked = a.Cluster + " is busy: " + busy
	}
	if p.Blocked == "" && !b.canStart(a) {
		p.Blocked = b.whyNot()
	}
	return p, nil
}

// Start implements state.Backend. With AllowChanges it starts a nodegroup
// roll; every other change is refused.
func (b *Backend) Start(ctx context.Context, a state.Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !b.opts.AllowChanges {
		return ErrReadOnly
	}
	if !b.canStart(a) {
		return errors.New(b.whyNot())
	}
	return b.startRoll(ctx, a)
}

// canStart reports whether this backend can start a.
func (b *Backend) canStart(a state.Action) bool {
	return b.opts.AllowChanges && a.Kind == state.ActionRoll
}

// whyNot says why a change cannot start here, and what to do instead.
func (b *Backend) whyNot() string {
	if !b.opts.AllowChanges {
		return ErrReadOnly.Error()
	}
	return "only nodegroup rolls start from the TUI so far: copy the CLI command (c) and run it in a shell"
}

// StopAfterCurrent implements state.Backend.
func (b *Backend) StopAfterCurrent(context.Context, string) error { return ErrReadOnly }

// TogglePause implements state.Backend.
func (b *Backend) TogglePause(context.Context, string) error { return ErrReadOnly }

var _ state.Backend = (*Backend)(nil)

// sortClusters orders the fleet by region, then name, like `refresh status`.
func sortClusters(cs []state.Cluster) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Region != cs[j].Region {
			return cs[i].Region < cs[j].Region
		}
		return cs[i].Name < cs[j].Name
	})
}

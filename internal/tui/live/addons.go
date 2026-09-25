package live

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// addonServices are the calls an add-on update makes. Tests replace them.
type addonServices struct {
	// preview is `addon update --all --dry-run --dependency-order`: the
	// add-ons that would change, in the order they would be updated.
	preview func(ctx context.Context, cfg aws.Config, cluster string) ([]addons.AddonUpdateResult, error)
	// update updates one add-on to version and waits for it, with the
	// pre-update ACTIVE check and the post-update health check.
	update func(ctx context.Context, cfg aws.Config, cluster, addon, version string, wait time.Duration) (*addons.AddonUpdateResult, error)
}

func defaultAddonServices(opts Options) addonServices {
	return addonServices{
		preview: func(ctx context.Context, cfg aws.Config, cluster string) ([]addons.AddonUpdateResult, error) {
			return factory.NewAddonService(cfg, opts.Logger).UpdateAll(ctx, cluster, addons.UpdateAllOptions{DryRun: true, DependencyOrder: true})
		},
		update: func(ctx context.Context, cfg aws.Config, cluster, addon, version string, wait time.Duration) (*addons.AddonUpdateResult, error) {
			return factory.NewAddonService(cfg, opts.Logger).Update(ctx, cluster, addon, addons.UpdateOptions{
				Version: version, HealthCheck: true, Wait: true, WaitTimeout: wait,
			})
		},
	}
}

// addonChange is one add-on a preview would change.
type addonChange struct{ name, from, to string }

// changesOf lists the add-ons a preview would change, in its order.
func changesOf(rows []addons.AddonUpdateResult) []addonChange {
	var out []addonChange
	for _, r := range rows {
		if r.Status == addons.StatusDryRun && r.NewVersion != "" && r.NewVersion != r.PreviousVersion {
			out = append(out, addonChange{name: r.AddonName, from: r.PreviousVersion, to: r.NewVersion})
		}
	}
	return out
}

// planAddonsLive replaces a sweep-based add-on plan with the service's own
// preview, which is exactly what Start will do, and records it as the plan
// the user confirms.
func (b *Backend) planAddonsLive(ctx context.Context, p *state.Plan, cfg aws.Config, t target) error {
	rows, err := b.addon.preview(ctx, cfg, t.name)
	if err != nil {
		return err
	}
	changes := changesOf(rows)
	p.Changes = nil
	for _, c := range changes {
		p.Changes = append(p.Changes, state.Change{Field: c.name, From: c.from, To: c.to})
	}
	p.Gates = nil
	for _, r := range rows {
		switch {
		case r.Warning != "":
			p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: r.AddonName, Note: r.Warning})
		case r.Failure != nil:
			p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: "could not plan " + r.AddonName, Note: r.Failure.Error})
		}
	}
	p.Gates = append(p.Gates,
		state.PlanGate{Status: state.CheckPending, Text: "each add-on must be ACTIVE", Note: "checked right before its update"},
		state.PlanGate{Status: state.CheckPending, Text: "post-update health", Note: "each add-on is waited for and checked"})
	p.Command = regionFlag(t) + "addon update --all -c " + t.name + " --dependency-order --health-check --wait"
	p.Blocked = ""
	if len(changes) == 0 {
		p.Blocked = "every add-on is on its newest compatible version"
	}
	b.mu.Lock()
	b.acceptedAddons[t] = changes
	b.mu.Unlock()
	return nil
}

// startAddons previews again and, when the plan is the one the user
// confirmed, updates the add-ons one at a time in the background.
func (b *Backend) startAddons(ctx context.Context, a state.Action) error {
	cfg, t, err := b.cfgFor(a.Cluster)
	if err != nil {
		return err
	}
	c, _ := b.cluster(a.Cluster)
	b.mu.Lock()
	if busy := b.busyOf(a.Cluster, c); busy != "" {
		b.mu.Unlock()
		return fmt.Errorf("%s is busy: %s", a.Cluster, busy)
	}
	accepted, planned := b.acceptedAddons[t]
	b.claimed[t] = "updating add-ons"
	b.mu.Unlock()
	started := false
	defer func() {
		if !started {
			b.mu.Lock()
			delete(b.claimed, t)
			b.mu.Unlock()
		}
	}()
	if !planned {
		return errors.New("no dry run for this update: open its dry run (a) first")
	}
	pctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
	rows, err := b.addon.preview(pctx, cfg, t.name)
	cancel()
	if err != nil {
		return err
	}
	changes := changesOf(rows)
	if !slices.Equal(changes, accepted) {
		return errors.New("the add-on plan changed since the dry run: open the dry run again (a)")
	}
	if len(changes) == 0 {
		return errors.New("every add-on is on its newest compatible version")
	}
	started = true

	b.mu.Lock()
	names := make([]string, len(changes))
	for i, ch := range changes {
		names[i] = ch.name
	}
	b.emit(state.Event{Cluster: a.Cluster, Source: state.SourceAddon, Level: state.LevelProgress, Subject: "add-ons", Text: "update started", Detail: strings.Join(names, " → ")})
	delete(b.acceptedAddons, t)
	runCtx := b.runCtx
	b.mu.Unlock()
	if runCtx == nil {
		runCtx = ctx
	}
	b.work.Add(1)
	go func() {
		defer b.work.Done()
		b.runAddons(runCtx, cfg, t, changes)
	}()
	return nil
}

// runAddons updates each add-on in turn, as `addon update --all` does: a
// failed add-on does not stop the others; a stopped run leaves the rest not
// attempted.
func (b *Backend) runAddons(ctx context.Context, cfg aws.Config, t target, changes []addonChange) {
	failed := 0
	for i, ch := range changes {
		if ctx.Err() != nil {
			b.mu.Lock()
			for _, rest := range changes[i:] {
				b.emit(state.Event{Cluster: b.keyOf(t), Source: state.SourceAddon, Level: state.LevelWarn, Subject: rest.name, Text: "not attempted", Detail: "the UI stopped"})
			}
			b.mu.Unlock()
			failed++
			break
		}
		b.mu.Lock()
		b.claimed[t] = fmt.Sprintf("updating add-ons · %s (%d/%d)", ch.name, i+1, len(changes))
		b.api("UpdateAddon", fmt.Sprintf("%s %s → %s", ch.name, ch.from, ch.to), state.LevelProgress)
		b.mu.Unlock()

		res, err := b.addon.update(ctx, cfg, t.name, ch.name, ch.to, b.opts.WaitTimeout)

		b.mu.Lock()
		key := b.keyOf(t)
		switch {
		case err != nil:
			failed++
			b.emit(state.Event{Cluster: key, Source: state.SourceAddon, Level: state.LevelError, Subject: ch.name, Text: "update failed", Detail: err.Error()})
		case res.Failed():
			failed++
			b.emit(state.Event{Cluster: key, Source: state.SourceAddon, Level: state.LevelError, Subject: ch.name, Text: string(res.Status), Detail: res.Failure.Error})
		case res.HealthIssues != "":
			failed++
			b.emit(state.Event{Cluster: key, Source: state.SourceAddon, Level: state.LevelWarn, Subject: ch.name, Text: "updated with health issues", Detail: res.HealthIssues})
		default:
			b.emit(state.Event{Cluster: key, Source: state.SourceAddon, Level: state.LevelOK, Subject: ch.name, Text: "ACTIVE " + res.NewVersion})
		}
		b.mu.Unlock()
	}
	b.mu.Lock()
	lvl, text := state.LevelOK, "add-ons updated"
	if failed > 0 {
		lvl, text = state.LevelWarn, fmt.Sprintf("add-on update finished · %d of %d need attention", failed, len(changes))
	}
	b.emit(state.Event{Cluster: b.keyOf(t), Source: state.SourceAddon, Level: lvl, Subject: "add-ons", Text: text})
	delete(b.claimed, t)
	b.mu.Unlock()
	b.Refresh()
}

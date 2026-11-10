package cmdutil

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// DefaultPollInterval is the default of --poll-interval.
const DefaultPollInterval = 15 * time.Second

// ChangeFlags are the values of the flags that every changing command
// has.
type ChangeFlags struct {
	DryRun, Yes, NoWait, Quiet      bool
	SkipHealthCheck, RequireHealthy bool
	// Force is set only by a command that has --force.
	Force                     bool
	WaitTimeout, PollInterval time.Duration
}

// ChangeFlagOptions say which of the shared flags a changing command has,
// and their defaults.
type ChangeFlagOptions struct {
	// Wait is the default of --wait-timeout, and WaitFor what that time
	// bounds, such as "each add-on".
	Wait    time.Duration
	WaitFor string
	// NoWait adds --no-wait. A command with later steps cannot return
	// early, and has none.
	NoWait bool
	// Force adds --force. A command that drains nothing has none.
	Force bool
}

// AddChangeFlags adds the shared flags of a changing command to cmd.
func AddChangeFlags(cmd *cobra.Command, f *ChangeFlags, opts ChangeFlagOptions) {
	fl := cmd.Flags()
	fl.BoolVarP(&f.DryRun, "dry-run", "d", false, "Print the plan and the verdict of the health gate, and change nothing")
	fl.BoolVarP(&f.Yes, "yes", "y", false, "Do not prompt; required with -o json|yaml and without a terminal")
	fl.BoolVarP(&f.Quiet, "quiet", "q", false, "Print no progress; questions, warnings, and the result still show")
	fl.DurationVar(&f.WaitTimeout, "wait-timeout", opts.Wait, "How long to wait for "+opts.WaitFor+"; 0 means no limit")
	fl.DurationVar(&f.PollInterval, "poll-interval", DefaultPollInterval, "Time between two status polls")
	fl.BoolVar(&f.SkipHealthCheck, "skip-health-check", false, "Skip the health gate before the change")
	fl.BoolVar(&f.RequireHealthy, "require-healthy", false, "Stop on any health warning, and do not ask")
	if opts.NoWait {
		fl.BoolVar(&f.NoWait, "no-wait", false, "Return once AWS accepted the change")
	}
	if opts.Force {
		fl.BoolVar(&f.Force, "force", false, "Proceed past drain blockers and a vCPU quota surge, with a warning")
	}
}

// Validate rejects the combinations of the shared flags that cannot work.
func (f *ChangeFlags) Validate() error {
	switch {
	case f.DryRun && f.NoWait:
		return errors.New("--dry-run changes nothing, so --no-wait has no effect with it; pass one of them")
	case f.PollInterval <= 0:
		return fmt.Errorf("invalid --poll-interval %s: must be more than 0", f.PollInterval)
	case f.WaitTimeout < 0:
		return fmt.Errorf("invalid --wait-timeout %s: must be 0 (no limit) or more", f.WaitTimeout)
	case f.SkipHealthCheck && f.RequireHealthy:
		return errors.New("--skip-health-check and --require-healthy cannot be used together: one skips the checks that the other requires")
	}
	return nil
}

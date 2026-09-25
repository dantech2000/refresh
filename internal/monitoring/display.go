package monitoring

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/render"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// newFrameDrawer returns the live region the progress view repaints. It is a
// var so tests can capture frames without a terminal.
var newFrameDrawer = func() refreshTypes.FrameDrawer {
	return render.Default(os.Stdout).NewLiveRegion(os.Stdout)
}

// displayTheme is the theme the progress and completion views use. It is a
// var so tests can pin the color level and glyph set.
var displayTheme = func() *render.Theme { return render.Default(os.Stdout) }

// DisplayProgressUpdate draws the current progress as a tree, one node per
// update. On a color terminal the frame is repainted in place
// (render.LiveRegion, which counts wrapped rows so no stale lines are left);
// when output is piped each poll appends a new block instead.
func DisplayProgressUpdate(monitor *refreshTypes.ProgressMonitor) {
	if monitor.Live == nil {
		monitor.Live = newFrameDrawer()
	}
	monitor.Live.Draw(progressLines(displayTheme(), monitor))
}

// progressLines builds one progress frame (pure, so tests can check it): the
// elapsed time, the cluster, and a tree node per update.
func progressLines(th *render.Theme, monitor *refreshTypes.ProgressMonitor) []string {
	out := []string{"Elapsed: " + time.Since(monitor.StartTime).Round(time.Second).String()}
	if len(monitor.Updates) > 0 {
		out = append(out, th.Bold(th.Pal.White, monitor.Updates[0].ClusterName))
		out = append(out, updateProgressTree(th, monitor.Updates)...)
	}
	return append(out, "")
}

// treePrefixes returns the branch for an update and the indent of its
// details.
func treePrefixes(last bool) (branch, indent string) {
	if last {
		return "└── ", "    "
	}
	return "├── ", "│   "
}

// updateToken is the status token of an update: its EKS status, or
// MONITORING FAILED when its status could not be polled (its outcome is
// unknown, which is not the EKS Failed status).
func updateToken(th *render.Theme, u refreshTypes.UpdateProgress) string {
	if u.MonitorErr != nil {
		return th.Tokenf(render.Unknown, monitoringFailedLabel)
	}
	return th.Tokenf(updateStatus(u.Status), statusLabel(u.Status))
}

// updateStatus maps an EKS update status to its render status.
func updateStatus(s types.UpdateStatus) render.Status {
	switch s {
	case types.UpdateStatusInProgress:
		return render.Progress
	case types.UpdateStatusSuccessful:
		return render.Healthy
	case types.UpdateStatusFailed:
		return render.Fail
	case types.UpdateStatusCancelled:
		return render.Warn
	default:
		return render.Unknown
	}
}

// statusLabel is the upper-case label of an EKS update status.
func statusLabel(s types.UpdateStatus) string {
	switch s {
	case "":
		return "UNKNOWN"
	case types.UpdateStatusInProgress:
		return "IN PROGRESS"
	default:
		return strings.ToUpper(string(s))
	}
}

// updateProgressTree renders the per-update nodes of a progress frame.
func updateProgressTree(th *render.Theme, updates []refreshTypes.UpdateProgress) []string {
	var out []string
	for i, u := range updates {
		last := i == len(updates)-1
		branch, indent := treePrefixes(last)
		out = append(out, branch+updateToken(th, u)+" "+th.Paint(th.Pal.White, u.NodegroupName))

		status := th.Tokenf(updateStatus(u.Status), string(u.Status))
		switch {
		case u.MonitorErr != nil:
			status = th.Tokenf(render.Unknown, monitoringFailedLabel+": "+firstLine(u.MonitorErr.Error()))
		case u.Status == types.UpdateStatusFailed || u.Status == types.UpdateStatusCancelled:
			if u.ErrorMessage != "" {
				status = th.Tokenf(updateStatus(u.Status), string(u.Status)+": "+u.ErrorMessage)
			}
		case u.ErrorMessage != "":
			// AWS reported errors but the update is not terminal yet.
			status += " " + th.Paint(th.Pal.Yellow, "(errors: "+u.ErrorMessage+")")
		case u.LastCheckError != "":
			// The status poll failed; the update itself may still be running.
			status += " " + th.Paint(th.Pal.Yellow, "(status check failing, retrying: "+u.LastCheckError+")")
		}

		out = append(out,
			indent+"├── Status: "+status,
			indent+"├── Duration: "+th.Paint(th.Pal.Text, time.Since(u.StartTime).Round(time.Second).String()),
			indent+"├── Update ID: "+th.Paint(th.Pal.Dim, u.UpdateID),
			indent+"└── Last Checked: "+th.Paint(th.Pal.Text, u.LastChecked.Format("15:04:05")),
		)
		// Spacing between nodegroups, except after the last one.
		if !last {
			out = append(out, "")
		}
	}
	return out
}

// DisplayCompletionSummary shows the final summary when all updates are
// complete. It replaces the last progress frame in place when there is one.
func DisplayCompletionSummary(monitor *refreshTypes.ProgressMonitor, config refreshTypes.MonitorConfig) error {
	if !config.Quiet {
		lines := completionLines(displayTheme(), monitor)
		if monitor.Live != nil {
			// The live region spaces frames itself: drop the leading blank.
			monitor.Live.Draw(lines[1:])
		} else {
			for _, l := range lines {
				fmt.Println(l)
			}
		}
	}

	// Return an error if any update did not succeed. Failures carry the AWS
	// error details so they surface even when the summary above was
	// suppressed. A Cancelled update is counted as failed above and must not
	// exit 0 (or trigger verification). An update that could not be
	// monitored has an unknown outcome and must not exit 0 either.
	var failures, unmonitored []string
	var monitorErrs []error
	cancelled := false
	for _, update := range monitor.Updates {
		if update.MonitorErr != nil {
			unmonitored = append(unmonitored, update.NodegroupName+" ("+update.UpdateID+")")
			monitorErrs = append(monitorErrs, update.MonitorErr)
			continue
		}
		switch update.Status {
		case types.UpdateStatusFailed:
			msg := update.NodegroupName
			if update.ErrorMessage != "" {
				msg += ": " + update.ErrorMessage
			}
			failures = append(failures, msg)
		case types.UpdateStatusCancelled:
			cancelled = true
		}
	}
	var errs []error
	switch {
	case len(failures) > 0:
		errs = append(errs, fmt.Errorf("one or more nodegroup updates failed: %s", strings.Join(failures, "; ")))
	case cancelled:
		errs = append(errs, fmt.Errorf("one or more nodegroup updates were cancelled"))
	}
	if len(unmonitored) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s; the EKS update(s) continue in the background: %w",
			ErrUnmonitored, strings.Join(unmonitored, ", "), errors.Join(monitorErrs...)))
	}
	return errors.Join(errs...)
}

// completionLines builds the completion summary (pure): the headline, the
// per-update tree, and the result counts.
func completionLines(th *render.Theme, monitor *refreshTypes.ProgressMonitor) []string {
	total := time.Since(monitor.StartTime).Round(time.Second)
	successful, failed, unmonitored := 0, 0, 0
	for _, u := range monitor.Updates {
		if u.MonitorErr != nil {
			unmonitored++
			continue
		}
		switch u.Status {
		case types.UpdateStatusSuccessful:
			successful++
		case types.UpdateStatusFailed, types.UpdateStatusCancelled:
			failed++
		}
	}

	out := []string{""}
	if unmonitored == 0 {
		out = append(out, fmt.Sprintf("All updates completed in %v", total))
	} else {
		out = append(out, fmt.Sprintf("Monitoring finished in %v", total))
	}
	out = append(out, "")
	if len(monitor.Updates) > 0 {
		out = append(out, th.Bold(th.Pal.White, monitor.Updates[0].ClusterName))
		out = append(out, completionSummaryTree(th, monitor.Updates)...)
	}

	results := fmt.Sprintf("Results: %s successful, %s failed",
		th.Paint(th.Pal.Green, fmt.Sprint(successful)), th.Paint(th.Pal.Red, fmt.Sprint(failed)))
	if unmonitored > 0 {
		results += fmt.Sprintf(", %s not monitored", th.Paint(th.Pal.Yellow, fmt.Sprint(unmonitored)))
	}
	return append(out, "", results)
}

// monitoringFailedLabel marks an update whose status could not be polled. It
// is distinct from the EKS FAILED status: the update's outcome is unknown.
const monitoringFailedLabel = "MONITORING FAILED"

// completionSummaryTree renders the per-update nodes of the completion
// summary.
func completionSummaryTree(th *render.Theme, updates []refreshTypes.UpdateProgress) []string {
	var out []string
	for i, u := range updates {
		last := i == len(updates)-1
		branch, indent := treePrefixes(last)
		out = append(out, branch+updateToken(th, u)+" "+th.Paint(th.Pal.White, u.NodegroupName))

		var status string
		switch {
		case u.MonitorErr != nil:
			status = th.Tokenf(render.Unknown, monitoringFailedLabel+": "+firstLine(u.MonitorErr.Error()))
		case u.Status == types.UpdateStatusFailed && u.ErrorMessage != "":
			status = th.Tokenf(render.Fail, "FAILED: "+u.ErrorMessage)
		default:
			status = th.Tokenf(updateStatus(u.Status), statusLabel(u.Status))
		}

		out = append(out,
			indent+"├── Status: "+status,
			indent+"├── Duration: "+th.Paint(th.Pal.Text, time.Since(u.StartTime).Round(time.Second).String()),
			indent+"└── Update ID: "+th.Paint(th.Pal.Dim, u.UpdateID),
		)
		if !last {
			out = append(out, "")
		}
	}
	return out
}

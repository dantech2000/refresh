package commands

import (
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
)

// exitCodeHelp lists, per command path (without the leading "refresh"), the
// exit codes the command can return, for its help text. An empty value
// means the command's description already lists its codes (or it is a
// group), so only the link to the contract is added. Every command must have
// an entry; TestEveryCommandDocumentsExitCodes enforces it. (REF-165)
var exitCodeHelp = map[string]string{
	"status": "",

	"cluster":               "",
	"cluster list":          "0 ok; 1 error, or no region answered; 4 incomplete: a region failed, or a cluster could not be fully read",
	"cluster describe":      "0 ok; 1 error; 4 incomplete: some add-ons or nodegroups could not be read",
	"cluster upgrade-check": "",
	"cluster upgrade":       "0 done, nothing to do, or a --dry-run with no blocker; 1 error, failed phase, interrupt, or timeout; 3 the plan has a blocker (also with --dry-run); 4 the planner could not read something",

	"nodegroup":          "",
	"nodegroup list":     "0 ok; 1 error; 4 incomplete: a nodegroup could not be described",
	"nodegroup describe": "0 ok; 1 error",
	"nodegroup scale":    "0 ok; 1 error, including a PDB check that could not run; 3 blocked by --check-pdbs or the pre-scaling health check, nothing changed; 4 --force scaled without being able to check the PDBs; 5 scaled, but the post-scaling health check found blocking issues",
	"nodegroup update":   "",

	"addon":            "",
	"addon list":       "0 ok; 1 error; 4 incomplete: an add-on could not be described",
	"addon describe":   "0 ok; 1 error",
	"addon update":     "0 ok; 1 error, interrupt, or a failed single-add-on update; 4 with --all, an add-on update failed or was not attempted, or an add-on could not be read after its update; 5 updated, but the post-update health check found issues",
	"addon update-all": "0 ok; 1 error or interrupt; 4 an add-on update failed, was not attempted, or could not be read after its update; 5 updated, but a post-update health check found issues",

	"use":            "0 ok; 1 error",
	"current":        "0 ok; 1 error",
	"context":        "",
	"context list":   "0 ok; 1 error",
	"context add":    "0 ok; 1 error",
	"context remove": "0 ok; 1 error",

	"version":     "0 ok; 1 error",
	"install-man": "0 ok; 1 error",
	"completion":  "0 ok; 1 error",
	"gen-docs":    "0 ok; 1 error",
	"ui":          "0 ok; 1 error, no interactive terminal, or no AWS credentials",
}

// DocumentExitCodes appends each command's exit codes and a link to the
// exit-code contract to its description, so `--help`, the man page, and the
// generated reference all show them. It returns the paths of commands
// without an exitCodeHelp entry.
func DocumentExitCodes(root *cli.Command) (missing []string) {
	var walk func(c *cli.Command, path string)
	walk = func(c *cli.Command, path string) {
		for _, sc := range c.Commands {
			if sc.Name == "help" {
				continue
			}
			p := strings.TrimSpace(path + " " + sc.Name)
			codes, ok := exitCodeHelp[p]
			if !ok {
				missing = append(missing, p)
			} else if !strings.Contains(sc.Description, runner.ExitCodesURL) {
				sc.Description = withExitCodes(sc.Description, codes)
			}
			walk(sc, p)
		}
	}
	walk(root, "")
	return missing
}

func withExitCodes(desc, codes string) string {
	line := "Exit codes: " + codes + ". See " + runner.ExitCodesURL
	if codes == "" {
		line = "Exit-code contract: " + runner.ExitCodesURL
	}
	desc = strings.TrimRight(desc, "\n ")
	if desc == "" {
		return line
	}
	return desc + "\n\n" + line
}

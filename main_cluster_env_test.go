package main

import (
	"slices"
	"testing"

	"github.com/urfave/cli/v3"
)

// EKS_CLUSTER_NAME is read only by `nodegroup update`, in code
// (updateClusterAndNodegroupPatterns), never as a flag source. As a flag
// source it would reach whichever command declares it, and urfave/cli reports
// an env-sourced --cluster as set, so the env var would beat a positional
// cluster and push it into the next slot. No flag on any command may name it,
// and no --cluster flag may have an env source at all.
func TestClusterEnvVarIsNotAFlagSource(t *testing.T) {
	var walk func(path string, c *cli.Command)
	walk = func(path string, c *cli.Command) {
		for _, f := range c.Flags {
			var keys []string
			var name string
			switch ff := f.(type) {
			case *cli.StringFlag:
				keys, name = ff.Sources.EnvKeys(), ff.Name
			case *cli.StringSliceFlag:
				keys, name = ff.Sources.EnvKeys(), ff.Name
			default:
				continue
			}
			if slices.Contains(keys, "EKS_CLUSTER_NAME") {
				t.Errorf("%s --%s reads EKS_CLUSTER_NAME as a flag source", path, name)
			}
			if name == "cluster" && len(keys) > 0 {
				t.Errorf("%s --cluster has env sources %v", path, keys)
			}
		}
		for _, sub := range c.Commands {
			walk(path+" "+sub.Name, sub)
		}
	}
	walk("refresh", newApp())
}

package main

import (
	"testing"

	"github.com/urfave/cli/v3"
)

// No --cluster flag may take its value from the environment: urfave/cli then
// reports the flag as set, so EKS_CLUSTER_NAME would beat a positional
// cluster and push it into the next slot. runner.RequestedCluster reads
// EKS_CLUSTER_NAME itself, after the positional.
func TestClusterFlagsHaveNoEnvSource(t *testing.T) {
	var walk func(path string, c *cli.Command)
	walk = func(path string, c *cli.Command) {
		for _, f := range c.Flags {
			sf, ok := f.(*cli.StringFlag)
			if !ok || sf.Name != "cluster" {
				continue
			}
			if keys := sf.Sources.EnvKeys(); len(keys) > 0 {
				t.Errorf("%s --cluster has env sources %v", path, keys)
			}
		}
		for _, sub := range c.Commands {
			walk(path+" "+sub.Name, sub)
		}
	}
	walk("refresh", newApp())
}

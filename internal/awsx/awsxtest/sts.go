package awsxtest

import "github.com/dantech2000/refresh/internal/fleet/fleettest"

var stsOperations = map[string]operation{
	"GetCallerIdentity": {handle: stsGetCallerIdentity},
}

func stsGetCallerIdentity(c *call) (any, error) {
	var out []node
	c.world.View(func(s *fleettest.State) {
		out = []node{leaf("Arn", s.CallerARN), leaf("UserId", s.UserID), leaf("Account", s.Account)}
	})
	return out, nil
}

package awsxtest

import "github.com/dantech2000/refresh/internal/fleet/fleettest"

var ssmOperations = map[string]operation{
	"GetParameter": {handle: ssmGetParameter},
}

func ssmGetParameter(c *call) (any, error) {
	name := c.in.str("Name")
	var value string
	var ok bool
	c.world.View(func(s *fleettest.State) { value, ok = s.Parameter(name) })
	if !ok {
		return nil, badRequest("ParameterNotFound", "Parameter "+name+" not found.")
	}
	return object{"Parameter": object{"Name": name, "Value": value, "Type": "String", "Version": 1}}, nil
}

package awsxtest

import "github.com/dantech2000/refresh/internal/fleet/fleettest"

var serviceQuotasOperations = map[string]operation{
	"GetServiceQuota": {handle: serviceQuotasGetServiceQuota},
}

func serviceQuotasGetServiceQuota(c *call) (any, error) {
	service, code := c.in.str("ServiceCode"), c.in.str("QuotaCode")
	var out any
	c.world.View(func(s *fleettest.State) {
		for _, q := range s.Quotas {
			if c.sees(q.Region) && q.ServiceCode == service && q.QuotaCode == code {
				out = object{"Quota": object{"ServiceCode": service, "QuotaCode": code, "Value": q.Value}}
			}
		}
	})
	if out == nil {
		return nil, newError("NoSuchResourceException", "The quota "+code+" of "+service+" does not exist.")
	}
	return out, nil
}

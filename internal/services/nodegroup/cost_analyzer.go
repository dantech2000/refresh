package nodegroup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"
	"github.com/dantech2000/refresh/internal/services/common"
)

type CostAnalyzer struct {
	pricing  *pricing.Client
	logger   *slog.Logger
	cache    *Cache
	cacheTTL time.Duration
	region   string
}

func NewCostAnalyzer(p *pricing.Client, logger *slog.Logger, cache *Cache, region string) *CostAnalyzer {
	return &CostAnalyzer{pricing: p, logger: logger, cache: cache, cacheTTL: 60 * time.Minute, region: region}
}

// EstimateOnDemandUSD returns monthly and per-hour costs for an instance type (Linux/on-demand)
func (c *CostAnalyzer) EstimateOnDemandUSD(ctx context.Context, instanceType string) (perHour float64, perMonth float64, ok bool) {
	if c.pricing == nil {
		c.logger.Debug("pricing client not available")
		return 0, 0, false
	}
	key := "price:" + c.region + ":" + instanceType
	if v, okc := c.cache.Get(key); okc {
		if cached, ok2 := v.(float64); ok2 {
			return cached, cached * 730.0, true
		}
	}

	location := regionToPricingLocation(c.region)
	if location == "" {
		c.logger.Debug("region not mapped to pricing location", "region", c.region)
		return 0, 0, false
	}

	// Try primary filter set first
	usd := c.queryPricing(ctx, instanceType, location, true)
	if usd <= 0 {
		// Try relaxed filter set (without capacitystatus which can be problematic)
		usd = c.queryPricing(ctx, instanceType, location, false)
	}

	if usd <= 0 {
		c.logger.Debug("could not determine pricing", "instanceType", instanceType, "location", location)
		return 0, 0, false
	}

	c.cache.Set(key, usd, c.cacheTTL)
	return usd, usd * 730.0, true
}

// queryPricing queries the AWS Pricing API for instance cost
func (c *CostAnalyzer) queryPricing(ctx context.Context, instanceType, location string, strict bool) float64 {
	svc := "AmazonEC2"
	filters := []pricingFilter{
		{Type: "TERM_MATCH", Field: "instanceType", Value: instanceType},
		{Type: "TERM_MATCH", Field: "location", Value: location},
		{Type: "TERM_MATCH", Field: "operatingSystem", Value: "Linux"},
		{Type: "TERM_MATCH", Field: "tenancy", Value: "Shared"},
		{Type: "TERM_MATCH", Field: "preInstalledSw", Value: "NA"},
	}

	if strict {
		filters = append(filters, pricingFilter{Type: "TERM_MATCH", Field: "capacitystatus", Value: "Used"})
	}

	input := &pricing.GetProductsInput{
		ServiceCode: aws.String(svc),
		Filters:     toPricingFilters(filters),
		MaxResults:  aws.Int32(10), // Get a few results in case first doesn't have pricing
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*pricing.GetProductsOutput, error) {
		return c.pricing.GetProducts(rc, input)
	})
	if err != nil {
		c.logger.Debug("pricing API error", "error", err, "instanceType", instanceType)
		return 0
	}
	if len(out.PriceList) == 0 {
		c.logger.Debug("no pricing data returned", "instanceType", instanceType, "location", location, "strict", strict)
		return 0
	}

	// Try to extract USD price from any of the returned products
	for _, priceItem := range out.PriceList {
		var doc map[string]any
		if err := json.Unmarshal([]byte(priceItem), &doc); err != nil {
			continue
		}
		onDemand := findPath(doc, "terms.OnDemand")
		if len(onDemand) == 0 {
			// Try lowercase
			onDemand = findPath(doc, "terms.onDemand")
		}
		usd := extractFirstUSD(onDemand)
		if usd > 0 {
			return usd
		}
	}

	return 0
}

// regionToPricingLocation maps AWS region to Pricing API location string
func regionToPricingLocation(region string) string {
	m := map[string]string{
		// US regions
		"us-east-1": "US East (N. Virginia)",
		"us-east-2": "US East (Ohio)",
		"us-west-1": "US West (N. California)",
		"us-west-2": "US West (Oregon)",
		// EU regions
		"eu-west-1":    "EU (Ireland)",
		"eu-west-2":    "EU (London)",
		"eu-west-3":    "EU (Paris)",
		"eu-central-1": "EU (Frankfurt)",
		"eu-central-2": "EU (Zurich)",
		"eu-north-1":   "EU (Stockholm)",
		"eu-south-1":   "EU (Milan)",
		"eu-south-2":   "EU (Spain)",
		// Asia Pacific regions
		"ap-east-1":      "Asia Pacific (Hong Kong)",
		"ap-southeast-1": "Asia Pacific (Singapore)",
		"ap-southeast-2": "Asia Pacific (Sydney)",
		"ap-southeast-3": "Asia Pacific (Jakarta)",
		"ap-southeast-4": "Asia Pacific (Melbourne)",
		"ap-northeast-1": "Asia Pacific (Tokyo)",
		"ap-northeast-2": "Asia Pacific (Seoul)",
		"ap-northeast-3": "Asia Pacific (Osaka)",
		"ap-south-1":     "Asia Pacific (Mumbai)",
		"ap-south-2":     "Asia Pacific (Hyderabad)",
		// Middle East regions
		"me-south-1":   "Middle East (Bahrain)",
		"me-central-1": "Middle East (UAE)",
		"il-central-1": "Israel (Tel Aviv)",
		// Africa regions
		"af-south-1": "Africa (Cape Town)",
		// Americas regions
		"ca-central-1": "Canada (Central)",
		"ca-west-1":    "Canada West (Calgary)",
		"sa-east-1":    "South America (Sao Paulo)",
	}
	return m[region]
}

// Helpers for light parsing
func findPath(doc map[string]any, path string) map[string]any {
	cur := doc
	for _, p := range strings.Split(path, ".") {
		if next, ok := cur[p].(map[string]any); ok {
			cur = next
		} else {
			return map[string]any{}
		}
	}
	return cur
}

func extractFirstUSD(onDemand map[string]any) float64 {
	for _, v := range onDemand {
		if term, ok := v.(map[string]any); ok {
			if pd, ok := term["priceDimensions"].(map[string]any); ok {
				for _, dim := range pd {
					if d, ok := dim.(map[string]any); ok {
						if unit, ok := d["pricePerUnit"].(map[string]any); ok {
							if usd, ok := unit["USD"].(string); ok {
								var f float64
								if _, err := fmt.Sscanf(usd, "%f", &f); err == nil {
									return f
								}
								return 0
							}
						}
					}
				}
			}
		}
	}
	return 0
}

// minimal filter wrapper to avoid leaking SDK types into callers
type pricingFilter struct{ Type, Field, Value string }

func toPricingFilters(fs []pricingFilter) []pricingtypes.Filter {
	out := make([]pricingtypes.Filter, 0, len(fs))
	for _, f := range fs {
		out = append(out, pricingtypes.Filter{Type: pricingtypes.FilterType(f.Type), Field: aws.String(f.Field), Value: aws.String(f.Value)})
	}
	return out
}

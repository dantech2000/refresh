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
		return 0, 0, false
	}
	svc := "AmazonEC2"
	filters := []pricingFilter{
		{Type: "TERM_MATCH", Field: "instanceType", Value: instanceType},
		{Type: "TERM_MATCH", Field: "location", Value: location},
		{Type: "TERM_MATCH", Field: "operatingSystem", Value: "Linux"},
		{Type: "TERM_MATCH", Field: "tenancy", Value: "Shared"},
		{Type: "TERM_MATCH", Field: "preInstalledSw", Value: "NA"},
		{Type: "TERM_MATCH", Field: "capacitystatus", Value: "Used"},
	}
	// Build input; Pricing SDK has typed filters, but we keep a light wrapper to avoid direct import of its filter type here.
	input := &pricing.GetProductsInput{ServiceCode: aws.String(svc), Filters: toPricingFilters(filters), MaxResults: aws.Int32(1)}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*pricing.GetProductsOutput, error) {
		return c.pricing.GetProducts(rc, input)
	})
	if err != nil || len(out.PriceList) == 0 {
		c.logger.Debug("pricing unavailable", "error", err)
		return 0, 0, false
	}
	// Parse the first price list entry
	var doc map[string]any
	if err := json.Unmarshal([]byte(out.PriceList[0]), &doc); err != nil {
		return 0, 0, false
	}
	// Traverse to onDemand -> priceDimensions -> pricePerUnit USD
	onDemand := findPath(doc, "terms.onDemand")
	usd := extractFirstUSD(onDemand)
	if usd <= 0 {
		return 0, 0, false
	}
	c.cache.Set(key, usd, c.cacheTTL)
	return usd, usd * 730.0, true
}

// regionToPricingLocation maps AWS region to Pricing API location string
func regionToPricingLocation(region string) string {
	m := map[string]string{
		"us-east-1":      "US East (N. Virginia)",
		"us-east-2":      "US East (Ohio)",
		"us-west-2":      "US West (Oregon)",
		"us-west-1":      "US West (N. California)",
		"eu-west-1":      "EU (Ireland)",
		"eu-west-2":      "EU (London)",
		"eu-central-1":   "EU (Frankfurt)",
		"ap-southeast-1": "Asia Pacific (Singapore)",
		"ap-southeast-2": "Asia Pacific (Sydney)",
		"ap-northeast-1": "Asia Pacific (Tokyo)",
		"ap-northeast-2": "Asia Pacific (Seoul)",
		"ap-south-1":     "Asia Pacific (Mumbai)",
		"ca-central-1":   "Canada (Central)",
		"sa-east-1":      "South America (São Paulo)",
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
								fmt.Sscanf(usd, "%f", &f)
								return f
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

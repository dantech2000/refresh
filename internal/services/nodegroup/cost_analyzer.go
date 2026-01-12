package nodegroup

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	pricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"
	"github.com/dantech2000/refresh/internal/services/common"
)

type CostAnalyzer struct {
	pricing   *pricing.Client
	ec2Client *ec2.Client
	logger    *slog.Logger
	cache     *Cache
	cacheTTL  time.Duration
	region    string
}

func NewCostAnalyzer(p *pricing.Client, logger *slog.Logger, cache *Cache, region string) *CostAnalyzer {
	return &CostAnalyzer{pricing: p, logger: logger, cache: cache, cacheTTL: 60 * time.Minute, region: region}
}

// SetEC2Client sets the EC2 client for spot pricing queries
func (c *CostAnalyzer) SetEC2Client(ec2Client *ec2.Client) {
	c.ec2Client = ec2Client
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

// SpotPriceResult contains spot pricing information
type SpotPriceResult struct {
	InstanceType     string    `json:"instanceType"`
	AvailabilityZone string    `json:"availabilityZone"`
	SpotPrice        float64   `json:"spotPrice"`
	Timestamp        time.Time `json:"timestamp"`
}

// EstimateSpotUSD returns current spot price for an instance type
func (c *CostAnalyzer) EstimateSpotUSD(ctx context.Context, instanceType string) (perHour float64, perMonth float64, ok bool) {
	if c.ec2Client == nil {
		c.logger.Debug("EC2 client not available for spot pricing")
		return 0, 0, false
	}

	key := "spot:" + c.region + ":" + instanceType
	if v, okc := c.cache.Get(key); okc {
		if cached, ok2 := v.(float64); ok2 {
			return cached, cached * 730.0, true
		}
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeSpotPriceHistoryOutput, error) {
		return c.ec2Client.DescribeSpotPriceHistory(rc, &ec2.DescribeSpotPriceHistoryInput{
			InstanceTypes:       []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
			ProductDescriptions: []string{"Linux/UNIX"},
			MaxResults:          aws.Int32(10),
		})
	})
	if err != nil || len(out.SpotPriceHistory) == 0 {
		c.logger.Debug("spot price unavailable", "instanceType", instanceType, "error", err)
		return 0, 0, false
	}

	// Calculate average spot price across availability zones
	var sum float64
	for _, price := range out.SpotPriceHistory {
		var p float64
		if _, err := fmt.Sscanf(aws.ToString(price.SpotPrice), "%f", &p); err == nil {
			sum += p
		}
	}
	avgSpotPrice := sum / float64(len(out.SpotPriceHistory))

	c.cache.Set(key, avgSpotPrice, 5*time.Minute) // Spot prices change frequently, shorter TTL
	return avgSpotPrice, avgSpotPrice * 730.0, true
}

// GetSpotPricesByAZ returns spot prices broken down by availability zone
func (c *CostAnalyzer) GetSpotPricesByAZ(ctx context.Context, instanceType string) ([]SpotPriceResult, error) {
	if c.ec2Client == nil {
		return nil, fmt.Errorf("EC2 client not available")
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeSpotPriceHistoryOutput, error) {
		return c.ec2Client.DescribeSpotPriceHistory(rc, &ec2.DescribeSpotPriceHistoryInput{
			InstanceTypes:       []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
			ProductDescriptions: []string{"Linux/UNIX"},
			MaxResults:          aws.Int32(20),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("fetching spot prices: %w", err)
	}

	// Deduplicate by AZ, keeping the most recent price
	azPrices := make(map[string]SpotPriceResult)
	for _, price := range out.SpotPriceHistory {
		az := aws.ToString(price.AvailabilityZone)
		var p float64
		if _, err := fmt.Sscanf(aws.ToString(price.SpotPrice), "%f", &p); err == nil {
			existing, exists := azPrices[az]
			if !exists || price.Timestamp.After(existing.Timestamp) {
				azPrices[az] = SpotPriceResult{
					InstanceType:     instanceType,
					AvailabilityZone: az,
					SpotPrice:        p,
					Timestamp:        aws.ToTime(price.Timestamp),
				}
			}
		}
	}

	results := make([]SpotPriceResult, 0, len(azPrices))
	for _, r := range azPrices {
		results = append(results, r)
	}
	return results, nil
}

// CalculateSpotSavings compares on-demand vs spot pricing and returns savings percentage
func (c *CostAnalyzer) CalculateSpotSavings(ctx context.Context, instanceType string) (savingsPercent float64, ok bool) {
	onDemandHourly, _, odOk := c.EstimateOnDemandUSD(ctx, instanceType)
	spotHourly, _, spotOk := c.EstimateSpotUSD(ctx, instanceType)

	if !odOk || !spotOk || onDemandHourly <= 0 {
		return 0, false
	}

	savings := (1 - spotHourly/onDemandHourly) * 100
	if savings < 0 || savings > 95 { // Sanity check
		return 0, false
	}
	return savings, true
}

// CostComparison contains a full cost comparison between capacity types
type CostComparison struct {
	InstanceType        string  `json:"instanceType"`
	OnDemandHourly      float64 `json:"onDemandHourly"`
	OnDemandMonthly     float64 `json:"onDemandMonthly"`
	SpotHourly          float64 `json:"spotHourly"`
	SpotMonthly         float64 `json:"spotMonthly"`
	SavingsPercent      float64 `json:"savingsPercent"`
	EstimatedRisk       string  `json:"estimatedRisk"` // low, medium, high based on spot frequency
	RecommendedCapacity string  `json:"recommendedCapacity"`
}

// CompareCosts provides a full comparison between on-demand and spot pricing
func (c *CostAnalyzer) CompareCosts(ctx context.Context, instanceType string, nodeCount int) (*CostComparison, error) {
	odHourly, odMonthly, odOk := c.EstimateOnDemandUSD(ctx, instanceType)
	spotHourly, spotMonthly, spotOk := c.EstimateSpotUSD(ctx, instanceType)

	result := &CostComparison{
		InstanceType: instanceType,
	}

	if odOk {
		result.OnDemandHourly = odHourly * float64(nodeCount)
		result.OnDemandMonthly = odMonthly * float64(nodeCount)
	}

	if spotOk {
		result.SpotHourly = spotHourly * float64(nodeCount)
		result.SpotMonthly = spotMonthly * float64(nodeCount)
	}

	if odOk && spotOk && odHourly > 0 {
		result.SavingsPercent = (1 - spotHourly/odHourly) * 100
	}

	// Estimate risk based on savings percentage (higher savings often means higher interruption risk)
	switch {
	case result.SavingsPercent > 80:
		result.EstimatedRisk = "high"
		result.RecommendedCapacity = "on-demand"
	case result.SavingsPercent > 60:
		result.EstimatedRisk = "medium"
		result.RecommendedCapacity = "mixed"
	default:
		result.EstimatedRisk = "low"
		result.RecommendedCapacity = "spot"
	}

	return result, nil
}

// staticPriceMap provides fallback prices for common instance types when API is unavailable
var staticPriceMap = map[string]float64{
	// General purpose
	"m5.large":   0.096,
	"m5.xlarge":  0.192,
	"m5.2xlarge": 0.384,
	"m5a.large":  0.086,
	"m5a.xlarge": 0.172,
	"m6i.large":  0.096,
	"m6i.xlarge": 0.192,
	"m6g.large":  0.077,
	"m6g.xlarge": 0.154,
	// Compute optimized
	"c5.large":   0.085,
	"c5.xlarge":  0.170,
	"c5.2xlarge": 0.340,
	"c6g.large":  0.068,
	"c6g.xlarge": 0.136,
	// Memory optimized
	"r5.large":   0.126,
	"r5.xlarge":  0.252,
	"r5.2xlarge": 0.504,
	"r6g.large":  0.101,
	"r6g.xlarge": 0.202,
	// Burstable
	"t3.micro":   0.0104,
	"t3.small":   0.0208,
	"t3.medium":  0.0416,
	"t3.large":   0.0832,
	"t3.xlarge":  0.1664,
	"t4g.micro":  0.0084,
	"t4g.small":  0.0168,
	"t4g.medium": 0.0336,
	"t4g.large":  0.0672,
}

// GetFallbackPrice returns a static price when API is unavailable
func (c *CostAnalyzer) GetFallbackPrice(instanceType string) (perHour float64, perMonth float64, ok bool) {
	if price, exists := staticPriceMap[instanceType]; exists {
		return price, price * 730.0, true
	}
	return 0, 0, false
}

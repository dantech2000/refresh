package awsx

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/middleware"
)

// Clients holds the service clients for one region, all with one retry
// policy. A Clients value is built once and is safe for concurrent use.
type Clients struct {
	Region string
	Policy Policy

	EKS           *eks.Client
	EC2           *ec2.Client
	AutoScaling   *autoscaling.Client
	SSM           *ssm.Client
	CloudWatch    *cloudwatch.Client
	ServiceQuotas *servicequotas.Client
	STS           *sts.Client

	provider *Provider
}

// With returns the clients for the same region with another retry policy. A
// wait loop that got Read clients calls With(Poll) for its polls.
func (c *Clients) With(policy Policy) *Clients {
	return c.provider.Clients(c.Region, policy)
}

// Provider hands out the Clients for a region and a retry policy. It builds
// each one on first use and keeps it. All of them share the HTTP client and
// the credentials of the configuration. A Provider is safe for concurrent
// use.
type Provider struct {
	cfg     aws.Config
	backoff retry.BackoffDelayer

	mu      sync.Mutex
	clients map[clientsKey]*Clients
}

type clientsKey struct {
	region string
	policy Policy
}

// ProviderOptions change how NewProvider builds clients.
type ProviderOptions struct {
	// Backoff replaces the delay between the attempts of a call. Nil means
	// exponential backoff with full jitter, capped at 5 seconds. A test
	// sets a zero delay.
	Backoff retry.BackoffDelayer
}

// NewProvider returns a provider of clients built from cfg.
func NewProvider(cfg aws.Config, optFns ...func(*ProviderOptions)) *Provider {
	var opts ProviderOptions
	for _, fn := range optFns {
		fn(&opts)
	}
	return &Provider{cfg: cfg, backoff: opts.Backoff, clients: map[clientsKey]*Clients{}}
}

// Region returns the region of the configuration: the home region of the
// run. It is empty when nothing sets a region.
func (p *Provider) Region() string {
	return p.cfg.Region
}

// Clients returns the clients for region with the retry policy. An empty
// region means the region of the configuration.
func (p *Provider) Clients(region string, policy Policy) *Clients {
	if region == "" {
		region = p.cfg.Region
	}
	key := clientsKey{region, policy}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[key]; ok {
		return c
	}

	cfg := p.cfg.Copy()
	cfg.Region = region
	// One retryer for the seven clients: they share its retry quota, as
	// the calls of one client do.
	retryer := newRetryer(policy, p.backoff)
	cfg.Retryer = func() aws.Retryer { return retryer }
	if policy == Sweep {
		cfg.APIOptions = append(cfg.APIOptions, addSweepDial)
	}
	c := &Clients{
		Region:        region,
		Policy:        policy,
		EKS:           eks.NewFromConfig(cfg),
		EC2:           ec2.NewFromConfig(cfg),
		AutoScaling:   autoscaling.NewFromConfig(cfg),
		SSM:           ssm.NewFromConfig(cfg),
		CloudWatch:    cloudwatch.NewFromConfig(cfg),
		ServiceQuotas: servicequotas.NewFromConfig(cfg),
		STS:           sts.NewFromConfig(cfg),
		provider:      p,
	}
	p.clients[key] = c
	return c
}

// addSweepDial adds a step that marks the context of each call, so that the
// shared HTTP client gives the call the short dial timeout of a sweep.
func addSweepDial(stack *middleware.Stack) error {
	return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("refresh:SweepDial",
		func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
			return next.HandleInitialize(withSweepDial(ctx), in)
		}), middleware.Before)
}

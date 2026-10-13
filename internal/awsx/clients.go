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
}

// SweepClients are the clients for the region callback of a default sweep:
// their policy is Sweep. A function that takes SweepClients says in its
// signature that it runs inside a sweep. It passes the embedded *Clients to
// code that reads one region and does not care which policy applies.
type SweepClients struct{ *Clients }

// PollClients are the clients for a wait loop: their policy is Poll. A
// function that takes PollClients says in its signature that it polls, and
// that it handles a transient error itself.
type PollClients struct{ *Clients }

// Provider hands out the clients for a region: Clients for ordinary calls,
// SweepClients for a sweep, PollClients for a wait loop. It builds each set
// on first use and keeps it. All of them share the HTTP client and
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

// Clients returns the clients for region with the Standard policy: for the
// describe, list, and update calls of a command. An empty region means the
// region of the configuration.
func (p *Provider) Clients(region string) *Clients {
	return p.clientsFor(region, Standard)
}

// SweepClients returns the clients for region with the Sweep policy.
func (p *Provider) SweepClients(region string) SweepClients {
	return SweepClients{p.clientsFor(region, Sweep)}
}

// PollClients returns the clients for region with the Poll policy.
func (p *Provider) PollClients(region string) PollClients {
	return PollClients{p.clientsFor(region, Poll)}
}

func (p *Provider) clientsFor(region string, policy Policy) *Clients {
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
	cfg.Retryer = func() aws.Retryer { return newRetryer(policy, p.backoff) }
	// AWS_MAX_ATTEMPTS and max_attempts in the shared config would replace
	// the attempt count of every policy, also the single attempt of Poll.
	// The retry mode has no effect next to a retryer; it is cleared so that
	// the configuration says what happens.
	cfg.RetryMaxAttempts = 0
	cfg.RetryMode = ""
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

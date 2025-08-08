# Enhanced Cluster Operations - refresh Tool

*Feature Specification v1.0*  
*Phase: 1 | Priority: HIGH | Effort: L*

## Executive Summary

**One-sentence value proposition**: Provide comprehensive cluster information and management capabilities with integrated health validation and cross-region support.

**Target Users**: DevOps Engineers, SREs, Platform Engineers

**Competitive Advantage**: Direct EKS API calls without CloudFormation overhead, real-time health integration, native multi-region operations

**Success Metric**: Achieve target response times with 95%+ user satisfaction on information completeness

## Problem Statement

### Current Pain Points
- **Slow workflows**: Multi-step commands and CloudFormation dependencies can add seconds to simple queries
- **Limited information**: Basic cluster info without operational context (health, costs, security posture)
- **Single region constraint**: Requires separate commands for different regions
- **No operational intelligence**: No integration with workload health, capacity planning, or optimization recommendations

### User Stories
```
As a DevOps engineer, I want to quickly see all my EKS clusters across regions with health status so that I can identify issues before they impact applications.

As an SRE, I want comprehensive cluster information including security configuration and capacity utilization so that I can make informed operational decisions.

As a Platform engineer, I want to compare clusters across environments and regions so that I can ensure consistency and identify configuration drift.
```

### Market Context
- **Limitations of common tools**: CloudFormation-based approaches can cause several-second delays for simple information retrieval
- **AWS CLI complexity**: Requires multiple commands and manual correlation to get comprehensive cluster view
- **User demand**: GitHub issues consistently request faster cluster operations and better information display

## Solution Overview

### Core Functionality
Enhanced cluster operations provide comprehensive, fast access to EKS cluster information through direct API calls. The feature integrates with the existing health check framework to provide operational intelligence beyond basic cluster metadata. This queries EKS APIs directly for near real-time response while providing richer information including health status, security configuration, and capacity insights.

Key capabilities include detailed cluster information with security and networking analysis, multi-region cluster discovery and comparison, integrated health validation using the existing health check framework, and performance optimization through caching and concurrent API calls.

### Key Benefits
1. **Performance**: Achieve 1-2 second responses for common operations
2. **User Experience**: Single command for comprehensive cluster analysis across regions
3. **Operational Value**: Integrated health checks and security posture analysis eliminate need for separate tools
4. **Strategic Advantage**: Foundation for all advanced refresh features, establishing pattern for operational excellence

## Command Interface Design

### Primary Commands
```bash
# Enhanced cluster information
refresh cluster describe -c my-cluster --detailed

# Cluster status with versions and health (new capability)
refresh cluster-status -c my-cluster --show-versions --show-endpoints

# Comprehensive health check (integrates existing health framework)
refresh cluster-health -c my-cluster --comprehensive

# Multi-cluster operations
refresh cluster list --all-regions --show-health

# Cluster comparison (new capability)
refresh cluster compare -c cluster1 -c cluster2
```

### Detailed Command Specifications

#### Command 1: `refresh cluster describe`
**Purpose**: Provide comprehensive cluster information with operational intelligence

**Syntax**:
```bash
refresh cluster describe -c CLUSTER [options]
```

**Required Arguments**:
- `-c, --cluster`: EKS cluster name or pattern

**Optional Flags**:
- `--detailed`: Show comprehensive information including networking, security, and capacity
- `--format`: Output format (table, json, yaml)
- `--show-health`: Include health status from existing health check framework
- `--show-costs`: Include cost analysis (requires Phase 3 cost feature)
- `--show-security`: Include security posture analysis

**Examples**:
```bash
# Basic cluster information
refresh cluster describe -c my-cluster

# Comprehensive view with health and security
refresh cluster describe -c my-cluster --detailed --show-health --show-security

# JSON output for automation
refresh cluster describe -c my-cluster --format json
```

**Output Format - Table View**:
```
Cluster Information: my-cluster
┌─────────────────┬───────────────────────────────────────┐
│ PROPERTY        │ VALUE                                 │
├─────────────────┼───────────────────────────────────────┤
│ Status          │ Active                                │
│ Version         │ 1.30                                  │
│ Platform        │ eks.15                                │
│ Endpoint        │ https://ABC123.gr7.us-west-2.eks... │
│ Health          │ ✅ Healthy (5/5 checks passed)       │
│ Nodegroups      │ 3 active (12 nodes total)            │
│ VPC             │ vpc-abc123 (10.0.0.0/16)            │
│ Subnets         │ 6 subnets across 3 AZs               │
│ Security Groups │ 2 groups (restrictive)               │
│ Logging         │ API, Audit enabled                   │
│ Encryption      │ ✅ At rest (KMS), ✅ In transit     │
│ Created         │ 2024-12-15 14:30:22 UTC              │
│ Age             │ 52 days                               │
└─────────────────┴───────────────────────────────────────┘

Add-ons:
┌─────────────────┬─────────┬────────────┬────────┐
│ NAME            │ VERSION │ STATUS     │ HEALTH │
├─────────────────┼─────────┼────────────┼────────┤
│ vpc-cni         │ v1.18.1 │ Active     │ ✅     │
│ coredns         │ v1.11.1 │ Active     │ ✅     │
│ kube-proxy      │ v1.30.0 │ Active     │ ✅     │
│ aws-ebs-csi     │ v1.30.0 │ Active     │ ✅     │
└─────────────────┴─────────┴────────────┴────────┘
```

**Output Format - JSON**:
```json
{
  "cluster": {
    "name": "my-cluster",
    "status": "Active",
    "version": "1.30",
    "platformVersion": "eks.15",
    "endpoint": "https://ABC123.gr7.us-west-2.eks.amazonaws.com",
    "health": {
      "status": "Healthy",
      "checks": {
        "passed": 5,
        "total": 5
      },
      "details": [...]
    },
    "networking": {
      "vpcId": "vpc-abc123",
      "subnetIds": ["subnet-123", "subnet-456"],
      "securityGroupIds": ["sg-789"]
    },
    "logging": {
      "enabled": ["api", "audit"],
      "disabled": ["authenticator", "controllerManager", "scheduler"]
    },
    "encryption": {
      "secrets": true,
      "provider": "arn:aws:kms:us-west-2:123456789012:key/abc123"
    },
    "addons": [...],
    "nodegroups": [...],
    "createdAt": "2024-12-15T14:30:22Z"
  }
}
```

#### Command 2: `refresh cluster list`
**Purpose**: Fast multi-region cluster discovery with health status

**Syntax**:
```bash
refresh cluster list [options]
```

**Optional Flags**:
- `--all-regions`: Query all EKS-supported regions
- `--region`: Specific region(s) to query
- `--show-health`: Include health status for each cluster
- `--show-costs`: Include cost information (requires Phase 3)
- `--filter`: Filter by status, version, or other criteria
- `--format`: Output format (table, json, yaml)

**Examples**:
```bash
# Fast local region cluster list
refresh cluster list

# Multi-region with health status
refresh cluster list --all-regions --show-health

# Filter by Kubernetes version
refresh cluster list --filter version=1.30
```

**Output Format - Table View**:
```
EKS Clusters (3 regions, 8 clusters)
┌────────────────┬────────────┬─────────┬─────────┬──────────┬─────────────────┐
│ CLUSTER        │ REGION     │ STATUS  │ VERSION │ HEALTH   │ NODES           │
├────────────────┼────────────┼─────────┼─────────┼──────────┼─────────────────┤
│ prod-api       │ us-west-2  │ Active  │ 1.30    │ ✅ Healthy│ 5/5 ready       │
│ prod-workers   │ us-west-2  │ Active  │ 1.30    │ ✅ Healthy│ 10/10 ready     │
│ staging-main   │ us-west-2  │ Active  │ 1.30    │ ⚠️  Warn  │ 3/3 ready       │
│ prod-api       │ us-east-1  │ Active  │ 1.30    │ ✅ Healthy│ 5/5 ready       │
│ dev-cluster    │ us-east-1  │ Active  │ 1.29    │ ✅ Healthy│ 2/2 ready       │
│ test-env       │ eu-west-1  │ Updating│ 1.30    │ 🔄 Update │ 4/4 ready       │
└────────────────┴────────────┴─────────┴─────────┴──────────┴─────────────────┘

Summary: 6 healthy, 1 warning, 1 updating
```

#### Command 3: `refresh cluster compare`
**Purpose**: Side-by-side cluster comparison for consistency validation

**Syntax**:
```bash
refresh cluster compare -c CLUSTER1 -c CLUSTER2 [options]
```

**Required Arguments**:
- `-c, --cluster`: Cluster name (can be specified multiple times)

**Optional Flags**:
- `--show-differences`: Highlight only differences
- `--include`: Compare specific aspects (networking, security, addons, versions)
- `--format`: Output format (table, json, yaml)

**Examples**:
```bash
# Basic cluster comparison
refresh cluster compare -c prod-us-west -c prod-us-east

# Focus on differences only
refresh cluster compare -c staging -c prod --show-differences

# Compare specific aspects
refresh cluster compare -c cluster1 -c cluster2 --include networking,security
```

## Technical Implementation

### AWS APIs Required
```go
// Primary APIs
"github.com/aws/aws-sdk-go-v2/service/eks"
- ListClusters
- DescribeCluster
- ListAddons
- DescribeAddon
- ListNodegroups

// Supporting APIs for comprehensive information
"github.com/aws/aws-sdk-go-v2/service/ec2"
- DescribeVpcs
- DescribeSubnets
- DescribeSecurityGroups

"github.com/aws/aws-sdk-go-v2/service/iam"
- GetRole (for cluster service role analysis)

// Existing APIs (already available)
"github.com/aws/aws-sdk-go-v2/service/sts"
- GetCallerIdentity (for credential validation)
```

### Data Structures
```go
// Enhanced cluster information
type ClusterDetails struct {
    // Basic cluster info
    Name              string                `json:"name"`
    Status            string                `json:"status"`
    Version           string                `json:"version"`
    PlatformVersion   string                `json:"platformVersion"`
    Endpoint          string                `json:"endpoint"`
    CreatedAt         time.Time             `json:"createdAt"`
    
    // Health information (integration with existing health framework)
    Health            *health.HealthStatus  `json:"health,omitempty"`
    
    // Networking details
    Networking        NetworkingInfo        `json:"networking"`
    
    // Security configuration
    Security          SecurityInfo          `json:"security"`
    
    // Add-ons and nodegroups
    Addons            []AddonInfo           `json:"addons"`
    Nodegroups        []NodegroupSummary    `json:"nodegroups"`
    
    // Operational metadata
    Tags              map[string]string     `json:"tags"`
    Region            string                `json:"region"`
}

type NetworkingInfo struct {
    VpcId             string   `json:"vpcId"`
    VpcCidr           string   `json:"vpcCidr"`
    SubnetIds         []string `json:"subnetIds"`
    SecurityGroupIds  []string `json:"securityGroupIds"`
    EndpointAccess    EndpointAccessInfo `json:"endpointAccess"`
}

type SecurityInfo struct {
    EncryptionEnabled bool   `json:"encryptionEnabled"`
    KmsKeyArn        string `json:"kmsKeyArn,omitempty"`
    ServiceRoleArn   string `json:"serviceRoleArn"`
    LoggingEnabled   []string `json:"loggingEnabled"`
}

type ClusterComparison struct {
    Clusters     []ClusterDetails `json:"clusters"`
    Differences  []Difference     `json:"differences"`
    Summary      ComparisonSummary `json:"summary"`
}
```

### Internal Service Interface
```go
package cluster

type Service interface {
    // Single cluster operations
    Describe(ctx context.Context, name string, options DescribeOptions) (*ClusterDetails, error)
    GetHealth(ctx context.Context, name string) (*health.HealthStatus, error)
    
    // Multi-cluster operations
    List(ctx context.Context, options ListOptions) ([]ClusterSummary, error)
    ListAllRegions(ctx context.Context, options ListOptions) ([]ClusterSummary, error)
    
    // Comparison operations
    Compare(ctx context.Context, clusterNames []string, options CompareOptions) (*ClusterComparison, error)
}

type ServiceImpl struct {
    eksClient     *eks.Client
    ec2Client     *ec2.Client
    iamClient     *iam.Client
    healthChecker *health.HealthChecker
    cache         *cache.Cache
    logger        *slog.Logger
}

type DescribeOptions struct {
    ShowHealth    bool
    ShowSecurity  bool
    ShowCosts     bool
    IncludeAddons bool
}

type ListOptions struct {
    Regions       []string
    ShowHealth    bool
    ShowCosts     bool
    Filters       map[string]string
}
```

### New Internal Packages
```
internal/
├── services/
│   └── cluster/
│       ├── service.go          # Main service implementation
│       ├── types.go           # Data structures
│       ├── client.go          # AWS client wrapper with retry logic
│       ├── cache.go           # Caching layer for performance
│       ├── comparison.go      # Cluster comparison logic
│       └── formatter.go       # Output formatting
└── commands/
    ├── describe_cluster.go    # describe-cluster command
    ├── list_clusters.go       # list-clusters command  
    └── compare_clusters.go    # compare-clusters command
```

### Configuration
```go
// Enhanced configuration for cluster operations
type ClusterConfig struct {
    CacheTimeout     time.Duration `yaml:"cache_timeout"`      // Default: 5 minutes
    MaxConcurrency   int          `yaml:"max_concurrency"`    // Default: 10
    DefaultFormat    string       `yaml:"default_format"`     // Default: "table"
    HealthChecks     bool         `yaml:"health_checks"`      // Default: true
    IncludeAddons    bool         `yaml:"include_addons"`     // Default: true
    MultiRegion      bool         `yaml:"multi_region"`       // Default: false
}
```

## Implementation Task Breakdown

### Phase 1: Infrastructure Setup (Estimated: 8 days)
- [ ] **Task 1.3**: Set up enhanced AWS SDK client configuration with retry logic
- [ ] **Task 1.5**: Create configuration management for cluster operations

### Phase 2: Core Implementation (Estimated: 12 days)
// Completed in codebase

### Phase 3: User Interface (Estimated: 10 days)  
- [ ] **Task 3.4**: Implement sorting for cluster lists (filtering implemented)
- [ ] **Task 3.5**: Add comprehensive help documentation and examples
- [ ] **Task 3.6**: Create interactive selection for cluster comparison

### Phase 4: Testing & Validation (Estimated: 15 days)
- [ ] **Task 4.1**: Write comprehensive unit tests (>90% coverage)
- [ ] **Task 4.2**: Create integration tests with real AWS EKS clusters
- [ ] **Task 4.3**: Implement performance benchmarks
- [ ] **Task 4.4**: Add multi-region testing and edge case validation
- [ ] **Task 4.5**: Security testing for credential handling
- [ ] **Task 4.6**: Load testing with large numbers of clusters
- [ ] **Task 4.7**: User acceptance testing with beta users

### Phase 5: Documentation & Launch (Estimated: 8 days)
- [ ] **Task 5.1**: Write comprehensive user documentation
- [ ] **Task 5.2**: Create migration guide from eksctl
- [ ] **Task 5.3**: Update CLI help and man pages
- [ ] **Task 5.4**: Prepare performance comparison demonstrations
- [ ] **Task 5.5**: Create demo videos showing speed improvements
- [ ] **Task 5.6**: Plan feature launch and user communication

## Dependencies & Prerequisites

### Feature Dependencies
- **Prerequisite Features**: None (foundation feature)
- **Parallel Development**: Can be developed alongside other Phase 1 features
- **Integration Points**: Existing health check framework for health status integration

### AWS Prerequisites
- **Required Permissions**: 
  ```json
  {
    "Version": "2012-10-17",
    "Statement": [
      {
        "Effect": "Allow",
        "Action": [
          "eks:ListClusters",
          "eks:DescribeCluster",
          "eks:ListAddons",
          "eks:DescribeAddon",
          "eks:ListNodegroups",
          "ec2:DescribeVpcs",
          "ec2:DescribeSubnets", 
          "ec2:DescribeSecurityGroups",
          "iam:GetRole"
        ],
        "Resource": "*"
      }
    ]
  }
  ```
- **Supported Regions**: All EKS-supported AWS regions
- **Kubernetes Versions**: All EKS-supported versions (1.25+)

### External Dependencies
- **kubectl**: Not required for basic functionality
- **AWS CLI**: Not required
- **Docker**: Not required

## Success Criteria

### Functional Requirements
- [ ] **Core Functionality**: All three commands work as designed across all EKS regions
- [ ] **Error Handling**: Graceful handling of network timeouts, permission errors, and invalid clusters
- [ ] **Performance**: 4x faster than eksctl for equivalent operations
- [ ] **Output Formats**: Complete support for table, JSON, and YAML formats
- [ ] **Health Integration**: Seamless integration with existing health check framework
- [ ] **Multi-Region**: Concurrent queries across regions with proper error isolation

### Non-Functional Requirements
- [ ] **Performance**: 
  - `describe-cluster` completes in <2 seconds (vs eksctl's 5-8 seconds)
  - `list-clusters --all-regions` completes in <5 seconds for up to 50 clusters
  - Multi-region queries use concurrent API calls for optimal performance
- [ ] **Reliability**: 99.9% success rate under normal AWS conditions
- [ ] **Usability**: 
  - Zero-config setup works with existing AWS credentials
  - Self-documenting with comprehensive help and examples
  - Consistent with existing refresh CLI patterns and user experience
- [ ] **Caching**: Intelligent caching reduces API calls while maintaining data freshness

### Quality Gates
- [ ] **Test Coverage**: >90% unit test coverage for all service components
- [ ] **Integration Testing**: Full test suite against real EKS clusters in multiple regions
- [ ] **Performance Testing**: Benchmarks demonstrate 4x improvement vs eksctl
- [ ] **Security Review**: No credential exposure, proper AWS SDK credential handling
- [ ] **User Validation**: Beta user feedback confirms usability and performance improvements
- [ ] **Documentation**: Complete CLI help, man pages, and user guides

## Risk Assessment & Mitigation

### Technical Risks
- **AWS API Rate Limits**: Multi-region queries may hit rate limits
  - *Impact*: Medium
  - *Likelihood*: Low
  - *Mitigation*: Implement exponential backoff, request batching, and intelligent caching

- **EKS API Changes**: AWS may modify EKS APIs affecting compatibility
  - *Impact*: High
  - *Likelihood*: Low
  - *Mitigation*: Monitor AWS SDK changelogs, implement adapter pattern for API changes

### Performance Risks
- **Network Latency**: Multi-region queries dependent on network performance
  - *Impact*: Medium
  - *Likelihood*: Medium
  - *Mitigation*: Concurrent queries, regional optimization, fallback mechanisms

- **Large Cluster Counts**: Performance may degrade with many clusters
  - *Impact*: Medium
  - *Likelihood*: Medium
  - *Mitigation*: Pagination, filtering, caching, and parallel processing

### Business Risks
- **User Adoption**: Users may prefer familiar eksctl despite performance benefits
  - *Mitigation*: Clear migration documentation, performance demonstrations, superior UX

- **eksctl Improvements**: AWS may improve eksctl performance
  - *Mitigation*: Focus on features eksctl can't provide (health integration, comparison, etc.)

## Metrics & KPIs

### Performance Metrics
- **Response Time**: Average time for describe-cluster operations (target: <2 seconds)
- **Multi-Region Performance**: Average time for all-regions queries (target: <5 seconds)
- **Success Rate**: Percentage of successful operations (target: >99%)
- **Cache Hit Rate**: Percentage of requests served from cache (target: >60%)

### Adoption Metrics
- **Feature Usage**: Number of users executing cluster operation commands
- **Command Frequency**: Most used commands and flag combinations
- **eksctl Migration**: Users switching from eksctl to refresh for cluster operations

### Quality Metrics
- **Error Rate**: Frequency of errors by type (network, permission, parsing)
- **User Satisfaction**: Survey feedback on speed and information quality
- **Support Requests**: Volume of user questions and issues related to cluster operations

---

This feature establishes the foundation for all advanced refresh capabilities while delivering immediate value through superior performance and comprehensive cluster intelligence.
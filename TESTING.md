# refresh CLI Testing Guide

This guide provides comprehensive testing instructions for the refresh CLI, including the new Enhanced Cluster Operations commands.

## Prerequisites

### Build the CLI
```bash
# Build the latest version
task build

# Or use quick development build
task dev
```

### AWS Credentials Setup
The refresh CLI requires valid AWS credentials. Choose one of these methods:

1. **AWS CLI (Recommended)**:
   ```bash
   aws configure
   ```

2. **Environment Variables**:
   ```bash
   export AWS_ACCESS_KEY_ID="your-access-key"
   export AWS_SECRET_ACCESS_KEY="your-secret-key"
   export AWS_DEFAULT_REGION="us-west-2"
   ```

3. **AWS SSO**:
   ```bash
   aws sso login --profile your-profile
   ```

4. **IAM Roles** (for EC2/EKS/Lambda environments)

## Testing Without AWS Credentials

### Command Help and Structure
```bash
# Test main help output
./dist/refresh --help

# Test individual command help
./dist/refresh describe-cluster --help
./dist/refresh dc --help  # Test alias
./dist/refresh list-clusters --help
./dist/refresh lc --help  # Test alias
./dist/refresh compare-clusters --help
./dist/refresh cc --help  # Test alias
```

### Input Validation
```bash
# Test required flag validation
./dist/refresh compare-clusters  # Should show error about required --cluster flag

# Test invalid output format
./dist/refresh describe-cluster -c test --format invalid  # Should show available formats
```

## Testing With AWS Credentials

### Basic Command Testing

#### describe-cluster (dc)
```bash
# Test basic describe (fastest test - single cluster)
./dist/refresh describe-cluster -c your-cluster-name

# Test with aliases and flags
./dist/refresh dc -c your-cluster-name
./dist/refresh dc -c your-cluster-name --detailed
./dist/refresh dc -c your-cluster-name --show-security
./dist/refresh dc -c your-cluster-name --format json
./dist/refresh dc -c your-cluster-name --format yaml
```

#### list-clusters (lc)
```bash
# Test single region listing
./dist/refresh list-clusters

# Test with aliases and filtering
./dist/refresh lc
./dist/refresh lc --show-health
./dist/refresh lc -H  # Short flag
./dist/refresh lc --format json
./dist/refresh lc -f name=prod  # Filter by name pattern

# Test multi-region (slower - queries all regions)
./dist/refresh lc --all-regions
./dist/refresh lc -A  # Short flag
./dist/refresh lc -r us-west-2 -r us-east-1  # Specific regions
```

#### compare-clusters (cc)
```bash
# Test cluster comparison (requires 2+ clusters)
./dist/refresh compare-clusters -c cluster1 -c cluster2
./dist/refresh cc -c cluster1 -c cluster2  # Alias
./dist/refresh cc -c cluster1 -c cluster2 --show-differences
./dist/refresh cc -c cluster1 -c cluster2 -i networking -i security
./dist/refresh cc -c cluster1 -c cluster2 --format json
```

### Advanced Testing Scenarios

#### Pattern Matching
```bash
# Test partial cluster name matching
./dist/refresh dc -c prod  # Should show matching clusters
./dist/refresh lc -f name=dev  # Should filter by pattern
```

#### Performance Validation
```bash
# Time the commands to verify performance claims
time ./dist/refresh lc --all-regions  # Should be significantly faster than eksctl
time ./dist/refresh dc -c your-cluster  # Should complete in ~1-2s
```

#### Error Handling
```bash
# Test with non-existent cluster
./dist/refresh dc -c non-existent-cluster

# Test with insufficient permissions
./dist/refresh lc --all-regions  # Test with limited IAM permissions
```

## Testing Infrastructure Requirements

### Minimal Testing (No AWS Resources)
- Test command help and structure
- Test input validation
- Test error messages

### Basic Testing (1 EKS Cluster)
- Test describe-cluster command
- Test list-clusters in single region
- Test output formats (table, JSON, YAML)

### Full Testing (2+ EKS Clusters)
- Test all commands including compare-clusters
- Test multi-region functionality
- Test filtering and pattern matching
- Test health integration (if health checker dependencies available)

### Multi-Region Testing (Clusters in Multiple Regions)
- Test --all-regions functionality
- Test region-specific queries
- Test performance at scale

## Expected Performance Benchmarks

### Performance Targets
- **describe-cluster**: < 2 seconds (vs eksctl: 5-8 seconds)
- **list-clusters (single region)**: < 3 seconds
- **list-clusters (all regions)**: < 10 seconds (vs eksctl: 30+ seconds)
- **compare-clusters**: < 5 seconds for 2 clusters

### Performance Testing
```bash
# Benchmark against eksctl
time eksctl get clusters --region us-west-2
time ./dist/refresh list-clusters

time eksctl get cluster your-cluster --region us-west-2
time ./dist/refresh describe-cluster -c your-cluster
```

## Output Validation

### Table Format
- Verify proper alignment with colored text
- Ensure consistent column widths
- Check box-drawing characters render correctly

### JSON/YAML Format
- Validate JSON syntax: `./dist/refresh lc --format json | jq .`
- Validate YAML syntax: `./dist/refresh lc --format yaml | yq .`

### Status Indicators
- Verify color coding (green=success, yellow=warning, red=error)
- Check text-based status indicators (PASS, WARN, FAIL)
- Ensure no emojis are used

## Automated Testing

### Unit Tests
```bash
# Run all unit tests
task test

# Run with coverage
task test-coverage

# Run comprehensive test suite
task test-suite
```

### Linting and Quality
```bash
# Run full development check
task dev-full-check

# Individual quality checks
task fmt      # Code formatting
task vet      # Go vet
task lint     # golangci-lint
```

### Build Validation
```bash
# Verify clean build
task clean && task build

# Test installation
task dev  # Installs to local Go bin
```

## Troubleshooting

### Common Issues

#### AWS Credential Errors
```
Error: AWS credential validation failed
```
**Solution**: Follow AWS credential setup instructions above

#### Region Access Denied
```
Error: Access Denied (403) when listing clusters in region us-west-1
```
**Solution**: Either configure credentials for that region or skip with region-specific flags

#### No Clusters Found
```
No EKS clusters found
```
**Solution**: Verify you have EKS clusters in the selected region(s)

### Debug Mode
```bash
# For additional debugging information
export AWS_SDK_LOAD_CONFIG=1
export AWS_PROFILE=your-profile

# Verify AWS configuration
aws sts get-caller-identity
```

## Success Criteria

### Phase 1 Feature Validation
- [ ] All three commands (describe-cluster, list-clusters, compare-clusters) work correctly
- [ ] Command aliases (dc, lc, cc) function properly  
- [ ] All output formats (table, JSON, YAML) render correctly
- [ ] Performance targets are met (4x faster than eksctl)
- [ ] Error handling provides clear, actionable messages
- [ ] Multi-region functionality works without issues
- [ ] Filtering and pattern matching work as expected
- [ ] Professional UI with proper color handling and no emojis

### Quality Assurance
- [ ] All unit tests pass (`task test`)
- [ ] Zero linting issues (`task lint`)
- [ ] Clean build process (`task build`)
- [ ] Comprehensive documentation updated

This testing guide ensures the Enhanced Cluster Operations features are thoroughly validated and ready for production use.
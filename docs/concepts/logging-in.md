# Logging in to AWS

refresh uses the standard AWS credential chain, the same one as the AWS CLI.
It never stores credentials. This page shows how to give refresh credentials
for each way of logging in, and what each login error means.

## Choose the profile

A profile in `~/.aws/config` holds one way of logging in. Tell refresh which
profile to use in one of these ways. The first match wins.

1. `--profile <name>` on the command, for one command.
2. A refresh context with a profile (`refresh context add … --profile
   <name>`, then `refresh use`), saved with its cluster and region. See
   [Contexts](contexts.md).
3. `export AWS_PROFILE=<name>`, for the shell.

If none of these names a profile, refresh uses access keys from the
environment, then the `[default]` profile, then the EC2 instance role.

!!! note "Access keys in the environment win over AWS_PROFILE"
    When `AWS_ACCESS_KEY_ID` is set, the AWS SDK uses those keys and ignores
    `AWS_PROFILE`, as the AWS CLI does. `--profile` still wins over the keys.
    To use a profile, unset `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`,
    or pass `--profile`.

## IAM Identity Center (SSO)

1. Set up the profile once with `aws configure sso`. It writes an
   `sso-session` section and a profile with `sso_session`,
   `sso_account_id`, `sso_role_name`, and `region`.
2. Log in with `aws sso login --profile <name>`. This saves a login token.
   It does not select the profile for other tools.
3. Run refresh with that profile: `refresh --profile <name> status`, or
   `export AWS_PROFILE=<name>`, or a context.

A login lasts as long as the session your administrator set. With an
`sso-session` profile, the AWS SDK renews an expired login by itself while
the session's refresh token is still valid. When the login cannot be renewed,
refresh stops and names the command to run: `aws sso login --profile
<name>`.

An older-style profile (with `sso_start_url` in the profile and no
`sso_session`) saves its login apart from the `sso-session` profiles, even
for the same portal. Log in with that profile itself.

## Access keys

Run `aws configure --profile <name>` to save the keys in
`~/.aws/credentials`, and pick the profile as above. For a CI job, set
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (and `AWS_SESSION_TOKEN` for
temporary keys) instead.

## A role to assume

A profile with `role_arn` and `source_profile` (or `credential_source`)
makes refresh call `sts:AssumeRole` first:

```ini
[profile eks-admin]
role_arn       = arn:aws:iam::111122223333:role/eks-admin
source_profile = base
region         = us-east-1
```

The AssumeRole call needs a region: the profile's `region`, `-r`, or
`AWS_REGION`. The role's trust policy must allow the source identity, and
the source identity's policy must allow `sts:AssumeRole` on the role.

## credential_process

A profile with `credential_process` runs that command for the keys. refresh
runs it as the AWS CLI does. If the command fails, refresh shows its exit
status.

## EC2, EKS pods, and Lambda

On AWS compute, the instance role (or the pod's IRSA or Pod Identity role)
gives the credentials. In a container on EC2, the instance metadata service
needs a hop limit of 2 or more.

## When the login fails

| The error starts with | What it means | What to do |
|---|---|---|
| `AWS profile "<name>" not found` | The profile is in neither `~/.aws/config` nor `~/.aws/credentials` | Check the name in the list the error shows, or create the profile |
| `no AWS credentials found` | No profile, keys, or role gave credentials. Off EC2, this usually means no profile was chosen | Pick the profile with `--profile` or `AWS_PROFILE`. On EC2, check the instance role |
| `not logged in to IAM Identity Center (SSO), or the login expired` | The SSO profile has no saved login, or it expired and could not be renewed | Run the `aws sso login --profile <name>` the error shows |
| `IAM Identity Center gives this user no access to the profile's role` | The profile's `sso_role_name` is not assigned to you in `sso_account_id` | Fix the profile; the AWS access portal lists your accounts and roles |
| `AWS credentials not configured or invalid` | AWS refused the keys | Fix the keys. A note says when `AWS_ACCESS_KEY_ID` hides `AWS_PROFILE` |
| `cannot assume the role of the AWS profile` | `sts:AssumeRole` was denied | Fix the role's trust policy, or the source identity's policy |
| `AWS region configuration issue` | No region for the call, for example a role profile with no `region` | Add `region` to the profile, pass `-r`, or set `AWS_REGION` |
| `AWS refused the request (HTTP 403) … could not be read` | A wrong secret access key, or a missing permission | Run `aws sts get-caller-identity` to tell which |
| `insufficient AWS permissions` | The identity lacks an IAM action | Grant the actions in [Required IAM permissions](configuration.md#required-iam-permissions) |

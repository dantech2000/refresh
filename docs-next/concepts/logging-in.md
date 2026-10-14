# Logging in to AWS

`refresh` uses the standard AWS credential chain, the same one as the AWS CLI.
It stores no credentials. Before its first AWS call, a command resolves the
credentials from that chain. If no source gives credentials, the command stops
and prints what to do.

## Choose the profile

`refresh` picks the AWS profile in this order:

1. `--profile <name>`, for one command.
2. `AWS_PROFILE`, for the shell.
3. The profile of the active [context](contexts.md).
4. The `default` profile.

```bash
refresh status --profile prod
export AWS_PROFILE=prod
refresh context add prod --cluster prod-eks --region us-east-1 --profile prod --use
```

## IAM Identity Center (SSO)

1. Create the profile once.

    ```bash
    aws configure sso --profile prod
    ```

2. Log in. Repeat this step when the session expires.

    ```bash
    aws sso login --profile prod
    ```

3. Run `refresh` with the profile.

    ```bash
    refresh status --profile prod
    ```

## Access keys

```bash
aws configure --profile prod
```

`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` in the environment beat a
profile. If the keys in the environment are stale, unset them.

## A role to assume

A profile with `role_arn` and `source_profile` works as it does in the AWS
CLI. The source identity needs `sts:AssumeRole` on the role, and the trust
policy of the role must name the source identity.

## EC2, EKS pods, and CI

On EC2, `refresh` uses the instance role. In an EKS pod, it uses IRSA or EKS
Pod Identity. In a container on EC2, the instance metadata service needs a hop
limit of 2 or more.

## If the login fails

| The error starts with | What it means | What to do |
|---|---|---|
| `AWS profile "<name>" not found` | The profile is in neither `~/.aws/config` nor `~/.aws/credentials`. | Check the name, or create the profile with the command that the error shows. |
| `no AWS credentials found` | No profile, key, or role gave credentials. | Choose a profile with `--profile` or `AWS_PROFILE`. On EC2, check the instance role. |
| `not logged in to IAM Identity Center (SSO), or the login expired` | The SSO profile has no saved login, or the login expired. | Run the `aws sso login` command that the error shows. |
| `IAM Identity Center gives this user no access to the profile's role` | The role of the profile is not assigned to you in that account. | Fix `sso_role_name` or `sso_account_id` in the profile. |
| `AWS credentials not configured or invalid` | AWS refused the keys. | Replace the keys. |
| `cannot assume the role of the AWS profile` | `sts:AssumeRole` was denied. | Fix the trust policy of the role, or the policy of the source identity. |
| `AWS region configuration issue` | The call has no region. | Pass `-r`, set `AWS_REGION`, or add `region` to the profile. |
| `insufficient AWS permissions` | The identity lacks an IAM action. The error names the action. | Grant the actions in [Required IAM permissions](configuration.md#required-iam-permissions). |

An example:

```text
Error: AWS profile "nope" not found
Cause: AWS_PROFILE names it, and it is in neither ~/.aws/config nor ~/.aws/credentials

Create it:
  aws configure sso --profile nope    an SSO profile (IAM Identity Center)
  aws configure --profile nope        an access key
```

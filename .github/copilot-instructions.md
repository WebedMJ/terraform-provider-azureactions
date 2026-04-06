# Copilot Instructions

## Purpose

This repository implements a **Terraform provider for Azure Actions** (`terraform-provider-azureactions`). It uses the Terraform 1.14+ `action` block capability to perform imperative operational tasks on Azure resources (e.g. triggering runbooks, pipelines, power operations) as part of Terraform workflows.

The provider is built with the [terraform-plugin-framework](https://github.com/hashicorp/terraform-plugin-framework) and the [go-azure-sdk](https://github.com/hashicorp/go-azure-sdk).

## Repository Layout

```
.
├── internal/
│   ├── clients/          # Azure client initialisation and config
│   ├── provider/         # Provider registration and configuration
│   ├── sdk/              # Shared action interfaces and helpers
│   └── services/         # One sub-package per Azure service area
│       ├── automation/   # Azure Automation actions
│       ├── compute/      # Azure Compute actions (placeholder)
│       └── devops/       # Azure DevOps actions
├── examples/             # Example Terraform configurations
├── main.go               # Provider entry point
├── Makefile              # Build, test and format targets
└── README.md
```

Each service area has:

- `registration.go` – implements `sdk.ServiceRegistration`, listing its actions
- One `*_action.go` file per action, implementing `sdk.Action`
- One `*_action_test.go` file per action with mock-based unit tests

## Provider Architecture

### Adding a New Action

1. Create `internal/services/<service>/<name>_action.go` implementing:
   - `Metadata()` – sets `response.TypeName` (e.g. `azureactions_<service>_<name>`)
   - `Schema()` – defines input attributes using `action/schema`
   - `Configure()` – calls `r.Defaults(ctx, request, response)` to populate `r.Client`
   - `Invoke()` – performs the Azure operation; use `response.SendProgress()` for status updates
2. Register the action in `internal/services/<service>/registration.go`
3. Register the service in `internal/provider/provider.go` → `SupportedServices()`
4. Add mock-based unit tests in `*_action_test.go` (see **Testing** below)
5. Add an HCL usage example under `examples/`

### Client (`internal/clients/client.go`)

`clients.Client` holds the Azure environment, ARM authorizer and the original config (for creating additional authorizers, e.g. for Azure DevOps). Use `auth.NewAuthorizerFromCredentials` with the relevant `environments.Api` to obtain authorizers for non-ARM services.

### Action Metadata (`internal/sdk/action.go`)

`sdk.ActionMetadata` provides `r.Client` (populated in `Configure`) and `r.SubscriptionID`. Embed it in every action struct.

## Security Critical Issues

Review every change for:

- Hardcoded secrets, API keys, PAT tokens, or credentials — these must never appear in source code
- Credentials passed in log messages or progress events — use `[REDACTED]` if auth details must appear
- Missing input validation before Azure API calls
- Context leaks: always call `defer cancel()` immediately after `context.WithTimeout`
- Error wrapping: always use `%w` in `fmt.Errorf` to preserve error chains for diagnostics

## Coding Standards

### Go Conventions

- Follow [Go code review guidelines](https://github.com/golang/go/wiki/CodeReviewComments) at all times
- **Acronyms in identifiers must be all-caps**: use `ID`, `URL`, `HTTP`, `API`, `ARM`, `PAT` — never `Id`, `Url`, `Http` etc. This applies to struct fields, method names, local variables, and function parameters. Examples: `SubscriptionID`, `TenantID`, `ClientID`, `OrganizationURL`
- Use `gofmt`-compliant formatting; run `make fmt` before committing

```go
// Avoid
type Account struct {
    SubscriptionId string
    TenantId       string
}

// Prefer
type Account struct {
    SubscriptionID string
    TenantID       string
}
```

### Terraform Best Practices

- Use `terraform-plugin-framework` types (`types.String`, `types.Bool`, etc.) in all model structs
- Mark sensitive attributes with `Sensitive: true` in **provider** and **resource** schemas (e.g. tokens, secrets). Action schemas (`action/schema`) do not support `Sensitive: true` — see **Security Defaults** below for the correct approach.
- All required fields validated in `Invoke` before making API calls
- Use `response.Diagnostics.AddError()` / `sdk.SetResponseErrorDiagnostic()` for errors
- Use `response.SendProgress()` to report long-running operation status

### Azure Best Practices

- Always research the **latest stable Azure REST API version** before implementing
- Use go-azure-sdk typed clients (`job.NewJobClientWithBaseURI`, etc.) for Azure RM resources
- For non-ARM Azure services (Azure DevOps, etc.) use `net/http` with proper auth headers
- Respect Azure throttling; consider retries for transient failures
- Use `context.WithTimeout` when `wait_for_completion = true`; always call `defer cancel()`
- Generate unique, human-readable job/run names (e.g. `"terraform-action-<unix-timestamp>"`)

### Security Defaults

- All credentials (PAT tokens, client secrets) must be `Sensitive: true` in **provider** and **resource** schemas; **Note:** `action/schema` attributes do not support `Sensitive: true` (this is a Terraform framework limitation for actions). Advise users to supply sensitive action values via Terraform sensitive variables (`sensitive = true`) or environment variables, and document this clearly.
- Default authentication: service principal via environment variables (`ARM_*`)
- Never log credential values; use `[REDACTED]` if auth details must appear in messages
- Prefer Managed Identity and Workload Identity Federation over long-lived secrets in docs/examples
- Default `wait_for_completion = false` to avoid blocking Terraform runs unintentionally
- Default `timeout_minutes = 30` when waiting, to prevent infinite hangs

### Testing

- Every action **must** have a `_test.go` file with mock-based unit tests
- Tests use `net/http/httptest.NewServer` as a mock Azure/DevOps API and a `mockAuthorizer` that returns a fake token (see existing tests for the pattern)
- Create a `newTestClient(serverURL string)` helper per package to build a `*clients.Client` pointing at the test server
- Test table entries should cover: success, job/run failure, timeout/error conditions
- Acceptance tests (requiring real Azure credentials) are tagged with `TF_ACC=1` and live in `*_acc_test.go` files
- If acceptance infrastructure is not yet available, keep `*_acc_test.go` files as explicit placeholders that `t.Skip(...)` unless `TF_ACC` is set
- Run unit tests: `make test` | Run acceptance tests: `make testacc`

### Scaffolding Alignment Learnings (Session)

- Follow the scaffolding pattern for provider startup and versioning: keep `var version = "dev"` in `main.go`, pass `provider.New(version)` to `providerserver.Serve`, and set `response.Version` in provider `Metadata()`
- Prefer protocol v6 server wiring (`providerserver.Serve`) for framework alignment
- Use `MarkdownDescription` instead of `Description` for provider/action schemas to support generated docs and consistent registry rendering
- Keep generated-doc workflow available via `tools/tools.go` and a `make generate` target (`go generate ./tools`)
- Maintain example coverage under `examples/` for each action to support docs and usability

### Local Development OS Awareness

- Before running local commands, check the developer OS/shell and choose compatible commands (PowerShell vs bash)
- On Windows PowerShell, do **not** assume Unix tools like `tail`/`grep` are available
- PowerShell-friendly equivalents:
  - `tail -n 20` -> `Select-Object -Last 20`
  - `grep "pattern"` -> `Select-String "pattern"`
  - `ls -lh` -> `Get-ChildItem` (or `ls`) and format output in PowerShell style
- Keep command examples OS-aware in docs, reviews, and troubleshooting notes to reduce friction for contributors

### Documentation

- Keep `README.md` up to date with every new action: schema, HCL example, auth options
- Update `examples/` with a working HCL snippet for each action

## Authentication

The provider supports:

| Method                     | Config / Env vars                                                                                 |
| -------------------------- | ------------------------------------------------------------------------------------------------- |
| Service Principal (secret) | `client_id`, `client_secret`, `tenant_id` / `ARM_CLIENT_ID`, `ARM_CLIENT_SECRET`, `ARM_TENANT_ID` |
| Environment variables only | `ARM_*` env vars without provider block config                                                    |

For Azure DevOps actions, additional auth methods are supported at the **action level**:

| Method                                             | Action attribute                    |
| -------------------------------------------------- | ----------------------------------- |
| Personal Access Token (PAT)                        | `personal_access_token` (sensitive) |
| Service Principal (reuses provider SP credentials) | `auth_method = "service_principal"` |

## Key Dependencies

| Package                                              | Purpose                         |
| ---------------------------------------------------- | ------------------------------- |
| `github.com/hashicorp/terraform-plugin-framework`    | Terraform provider SDK          |
| `github.com/hashicorp/go-azure-sdk/resource-manager` | Azure RM typed API clients      |
| `github.com/hashicorp/go-azure-sdk/sdk`              | Auth, environments, HTTP client |

## Resources

- [Terraform Plugin Framework docs](https://developer.hashicorp.com/terraform/plugin/framework)
- [Terraform Actions (1.14)](https://developer.hashicorp.com/terraform/language/actions)
- [Azure REST API reference](https://learn.microsoft.com/en-us/rest/api/azure/)
- [go-azure-sdk](https://github.com/hashicorp/go-azure-sdk)
- [Azure DevOps REST API](https://learn.microsoft.com/en-us/rest/api/azure/devops/)

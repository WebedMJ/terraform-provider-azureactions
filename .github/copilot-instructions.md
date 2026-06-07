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
│   ├── acctest/          # Shared acceptance test helpers (build tag: acceptance)
│   └── services/         # One sub-package per Azure service area
│       ├── automation/   # Azure Automation actions
│       ├── eventgrid/    # Azure Event Grid actions
│       └── devops/       # Azure DevOps actions
├── examples/             # Example Terraform configurations (used by tfplugindocs)
├── tools/                # go generate directive for tfplugindocs
├── docs/                 # Generated provider documentation (do not edit manually)
├── main.go               # Provider entry point
├── Makefile              # Build, test and format targets
└── README.md
```

Each service area has:

- `registration.go` – implements `sdk.ServiceRegistration` (single method: `Actions()`)
- One `*_action.go` file per action, implementing `sdk.Action`
- One `*_action_test.go` file per action with mock-based unit tests
- One `*_action_acc_test.go` file per action for acceptance tests (build tag: `acceptance`)

## Provider Architecture

### Adding a New Action

1. Create `internal/services/<service>/<name>_action.go` implementing:
   - `Metadata()` – sets `response.TypeName` (e.g. `azureactions_<service>_<name>`)
   - `Schema()` – defines input attributes using `action/schema` with `MarkdownDescription`
   - `Configure()` – calls `r.Defaults(ctx, request, response)` to populate `r.Client`
   - `Invoke()` – performs the Azure operation; use `response.SendProgress()` for status updates
2. Register the action in `internal/services/<service>/registration.go`
3. Register the service in `internal/provider/provider.go` → `SupportedServices()`
4. Add mock-based unit tests in `*_action_test.go` (see **Testing** below)
5. Add acceptance tests in `*_action_acc_test.go`
6. Add an HCL usage example under `examples/actions/<service>_<name>/action.tf`

### `ServiceRegistration` interface (`internal/sdk/service_registration.go`)

```go
type ServiceRegistration interface {
    Actions() []func() action.Action
}
```

**Important:** `ServiceRegistration` intentionally has only `Actions()`. Do **not** add a `Name()` method — it is unused at current provider scale and violates YAGNI. If per-service tooling (test filtering, log output) is needed in future, add it then.

### Client (`internal/clients/`)

`clients.Client` is the central object passed from the provider to every action via `ActionMetadata.Client`. It contains:

| Field         | Type                        | Purpose                                                                          |
| ------------- | --------------------------- | -------------------------------------------------------------------------------- |
| `Account`     | `clients.Account`           | Subscription ID, Tenant ID, Client ID, environment, org URL (read-only snapshot) |
| `Config`      | `clients.Config`            | Full config including `ClientSecret` and `OrganizationURL`                       |
| `Environment` | `*environments.Environment` | Resolved Azure cloud environment (endpoints)                                     |
| `Authorizer`  | `auth.Authorizer`           | ARM-scoped authorizer (for go-azure-sdk RM clients)                              |
| `Credential`  | `azcore.TokenCredential`    | Raw token credential (for direct token acquisition)                              |

**Authentication model in `clients.NewClient`:**

The credential is created with two hard paths — no chaining, no silent fallback:

- If **all three** of `TenantID` + `ClientID` + `ClientSecret` are non-empty → `azidentity.NewClientSecretCredential`. DAC is not used. Bad credentials fail immediately.
- Otherwise → `azidentity.NewDefaultAzureCredential` (Azure CLI, workload identity, managed identity, env vars, etc.). `TenantID` is passed as a hint if present.

**Do not** replace this with `NewChainedTokenCredential`. The two-path approach is intentional: explicit credentials fail fast rather than silently falling through to DAC.

**Obtaining authorizers for non-ARM APIs (e.g. Azure DevOps):**

Use `c.Client.Credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"<scope>"}})` directly — **not** `auth.NewAuthorizerFromCredentials`. The DevOps action uses the well-known scope `499b84ac-1321-427f-aa17-267ca6975798/.default`.

`c.Client.AuthorizerFor(api)` wraps `NewTokenCredentialAuthorizer` for go-azure-sdk `auth.Authorizer` consumers.

**`tokenCredentialAuthorizer` (`internal/clients/token_credential_authorizer.go`):**

Bridges `azcore.TokenCredential` to `auth.Authorizer`. The `AuxiliaryTokens` method returns `nil, nil` — this is correct and intentional (no cross-tenant auth required). Do not change it.

### Action Metadata (`internal/sdk/action.go`)

`sdk.ActionMetadata` provides `r.Client` and `r.SubscriptionID` via `Defaults()`. Embed it in every action struct.

```go
type RunbookTriggerAction struct {
    sdk.ActionMetadata
    pollInterval time.Duration // override in tests; zero = default
}
```

`r.SubscriptionID` is a convenience shortcut for `r.Client.Account.SubscriptionID`.

## Provider Configuration

### Env var precedence (all credentials)

For every configurable field the resolution order is:

1. Provider block value (explicit HCL)
2. `AZURE_*` environment variable
3. `ARM_*` environment variable (alias, equal weight — not deprecated)

Implemented via `providerValueOrEnv()` in `internal/provider/provider.go`. **Do not** change this precedence.

| Provider attribute | Primary env var         | Alias env var         |
| ------------------ | ----------------------- | --------------------- |
| `subscription_id`  | `AZURE_SUBSCRIPTION_ID` | `ARM_SUBSCRIPTION_ID` |
| `client_id`        | `AZURE_CLIENT_ID`       | `ARM_CLIENT_ID`       |
| `client_secret`    | `AZURE_CLIENT_SECRET`   | `ARM_CLIENT_SECRET`   |
| `tenant_id`        | `AZURE_TENANT_ID`       | `ARM_TENANT_ID`       |
| `organization_url` | `AZUREDEVOPS_ORG_URL`   | _(none)_              |
| `environment`      | `ARM_ENVIRONMENT`       | _(none)_              |

### `subscription_id` is optional at provider level

`subscription_id` is **not** required by the provider — some actions (e.g. DevOps PAT auth) don't need it. Actions that require it validate it themselves in `Invoke()` using `r.SubscriptionID == ""`.

### `organization_url` is provider-level for DevOps

The DevOps `organization_url` is configured once on the provider block (or via `AZUREDEVOPS_ORG_URL`), not per-action. The `PipelineTriggerAction` retrieves it from `p.Client.Config.OrganizationURL` via `p.organizationURL()`.

### Validation at Configure time

Only validate cross-field constraints at provider `Configure()` time, e.g.:

- `client_secret` present but `client_id` or `tenant_id` missing → error immediately.
- **Do not** validate subscription ID or org URL at Configure time — actions validate what they actually need.

## Security Critical Issues

Review every change for:

- Hardcoded secrets, API keys, PAT tokens, or credentials — these must never appear in source code
- Credentials in log messages or progress events — use `[REDACTED]` if auth details must appear
- Missing input validation before Azure API calls
- Context leaks: always call `defer cancel()` immediately after `context.WithTimeout`
- Error wrapping: always use `%w` in `fmt.Errorf` to preserve error chains for diagnostics
- PAT tokens in action schema — `action/schema` does not support `Sensitive: true`; document that users must use Terraform sensitive variables

## Coding Standards

### Go Conventions

- Follow [Go code review guidelines](https://github.com/golang/go/wiki/CodeReviewComments) at all times
- **Acronyms in identifiers must be all-caps**: `ID`, `URL`, `HTTP`, `API`, `ARM`, `PAT` — never `Id`, `Url`, `Http` etc. Applies to struct fields, method names, local variables, function parameters. Examples: `SubscriptionID`, `TenantID`, `ClientID`, `OrganizationURL`
- Use `gofmt`-compliant formatting; run `make fmt` before committing
- No speculative abstractions — only add interfaces/helpers when something uses them (YAGNI)

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

### Terraform Plugin Framework Patterns

- Use `terraform-plugin-framework` types (`types.String`, `types.Bool`, etc.) in all model structs
- Mark sensitive attributes with `Sensitive: true` in **provider** schemas (e.g. `client_secret`). Action schemas (`action/schema`) do **not** support `Sensitive: true` — advise users to use Terraform sensitive variables.
- Validate all required fields at the start of `Invoke()` before making any API calls
- Use `sdk.SetResponseErrorDiagnostic(response, summary, detail)` for errors in `Invoke()` — it accepts `string` or `error`
- Use `response.Diagnostics.AddError()` for errors in `Configure()`
- Use `response.SendProgress()` for progress events during long-running operations
- Use `MarkdownDescription` (not `Description`) for all schema attributes

### Invoke() structure

Each `Invoke()` should follow this order:

1. Decode model: `request.Config.Get(ctx, &model)`
2. Validate action-level required fields (subscription, org URL, etc.)
3. Build clients / resolve auth
4. Validate business inputs (pipeline_id > 0, timeout >= 1, etc.)
5. Send initial progress event
6. Execute the Azure/DevOps operation
7. Send completion / wait loop with ticker + context cancellation

### Polling pattern (wait_for_completion)

```go
// Always: validate timeout before creating the context
if timeoutMinutes < 1 {
    sdk.SetResponseErrorDiagnostic(...)
    return
}
waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMinutes)*time.Minute)
defer cancel()

ticker := time.NewTicker(interval)
defer ticker.Stop()

for {
    select {
    case <-waitCtx.Done():
        // surface ctx.Err() with %w
    case <-ticker.C:
        // poll and handle terminal states
    }
}
```

Default `timeout_minutes = 30` when not provided. Default poll interval = 10s for Automation, 15s for DevOps.

### Azure REST API versioning

When importing a go-azure-sdk API package, add a comment with the version, review date, and reference URL:

```go
// Azure Automation API version 2024-10-23 provides current support for managing jobs and runbooks.
// Last reviewed: 2026-06-03
// Ref: https://learn.microsoft.com/en-us/rest/api/automation/
"github.com/hashicorp/go-azure-sdk/resource-manager/automation/2024-10-23/job"
```

Always use the **latest stable** API version. Research it before implementing.

For non-ARM APIs (DevOps, etc.) document the version in a constant:

```go
const (
    // Azure DevOps REST API version 7.1 is the current stable release.
    // Last reviewed: 2024-12-20
    // Ref: https://learn.microsoft.com/en-us/rest/api/azure/devops/
    devOpsAPIVersion = "7.1"
)
```

### Azure DevOps HTTP client

The DevOps action uses `net/http` directly. The default HTTP client (when no test override is set) must have **explicit timeouts** — never use `http.DefaultClient` in production paths:

```go
return &http.Client{
    Timeout: 30 * time.Second,
    Transport: &http.Transport{
        DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
        TLSHandshakeTimeout: 10 * time.Second,
        ...
    },
}
```

### Progress events — result detail pattern

Actions emit a structured "result details" progress event after key operations to surface IDs/state that Terraform currently cannot expose as outputs on actions:

```go
response.SendProgress(action.InvokeProgressEvent{
    Message: fmt.Sprintf("Action result details (progress-only): job_name=%s initial_status=%s", jobName, status),
})
```

This is the approved pattern until Terraform actions support output attributes.

### Security Defaults

- All credentials (`client_secret`, PAT tokens) must be `Sensitive: true` in provider schemas
- `action/schema` does not support `Sensitive: true` — document clearly; advise sensitive Terraform variables
- Default `wait_for_completion = false`
- Default `timeout_minutes = 30`
- Never log credential values; use `[REDACTED]` if auth details must appear in messages
- Prefer Managed Identity and Workload Identity Federation in docs/examples

## Testing

### Unit tests

- Every action **must** have a `_test.go` file with mock-based unit tests
- Tests use `net/http/httptest.NewServer` as a mock API and implement `auth.Authorizer` / `azcore.TokenCredential` locally
- Each test package defines:
  - `newTestClient(...)` — builds `*clients.Client` with the test server URL wired in
  - `newTestAction(server)` — creates the action struct with short `pollInterval` and test client configured
  - `buildXxxConfig(t, ...)` — constructs `tfsdk.Config` using `tftypes` directly (no HCL parsing)
  - `invokeXxxAction(t, a, cfg)` — calls `Invoke()` with a 10s context timeout, captures progress messages

- For DevOps tests that test `default_azure_credential` / `service_principal` auth, use `mockAzureTokenCredential` implementing `azcore.TokenCredential` — do NOT use `devOpsAuthorizerFactory` (that pattern was removed; auth now goes directly via `p.Client.Credential.GetToken`)
- Test coverage must include: success, job/run failure, timeout/context cancellation, invalid inputs

### Acceptance tests

- Live in `*_action_acc_test.go` with `//go:build acceptance` tag
- Shared helpers in `internal/acctest/acctest.go` (same build tag)
- `PreCheckCommon()` requires `AZURE_SUBSCRIPTION_ID` or `ARM_SUBSCRIPTION_ID`
- `PreCheckAutomation()` additionally requires `ACC_TEST_RG`, `ACC_TEST_AUTOMATION_ACCOUNT`, `ACC_TEST_RUNBOOK_NAME`
- `PreCheckDevOps()` requires `AZUREDEVOPS_ORG_URL`, `AZUREDEVOPS_PROJECT`, `AZUREDEVOPS_PIPELINE_ID` — **does not require subscription** (DevOps doesn't need it)
- `ProviderConfigFromEnv(t)` emits a minimal provider block; subscription and environment are omitted when not set

### Running tests

```bash
make test        # unit tests (no Azure credentials needed)
make testacc     # acceptance tests (requires real infrastructure + TF_ACC=1)
make vet         # go vet
make fmt         # gofmt
make generate    # regenerate docs via tfplugindocs
```

The Makefile uses `TEST?=./...` (cross-platform). On Windows use PowerShell for make equivalents:

- `go test ./... -v` instead of `make test` if make is unavailable
- `gofmt -s -w .` instead of `make fmt`

## Local Development OS Awareness

The primary development environment is **Windows / PowerShell**. Do not assume Unix tools.

| Unix               | PowerShell equivalent                |
| ------------------ | ------------------------------------ |
| `tail -n 20`       | `Select-Object -Last 20`             |
| `grep "pattern"`   | `Select-String "pattern"`            |
| `ls -lh`           | `Get-ChildItem`                      |
| `find . -name ...` | `Get-ChildItem -Recurse -Filter ...` |
| `export VAR=val`   | `$env:VAR = "val"`                   |

The Makefile `fmt` target uses `gofmt -s -w .` (works on both platforms when gofmt is in PATH). The `fmtcheck` target uses `sh` — only works in a Unix-like shell; use `gofmt -l .` directly on Windows.

## Documentation

- Keep `README.md` up to date for every new action: schema table, HCL example, auth options, env vars
- Add an `examples/actions/<service>_<name>/action.tf` example for every action (used by tfplugindocs)
- Run `make generate` after schema changes to update `docs/`
- Documentation is generated from schema `MarkdownDescription` + example `.tf` files — keep both accurate

## Session Learnings (2026-06)

- Event Grid action naming: use `azureactions_eventgrid_publish_cloudevent` (not `...publish_event`) to make supported payload schema explicit.
- Event Grid payload support: this provider action publishes **CloudEvents only**. Document this clearly in schema descriptions, README, examples, and generated docs.
- Event Grid resource schema requirement: target topics/domains must accept CloudEvents input schema (for example `input_schema = "CloudEventSchemaV1_0"` in `azurerm_eventgrid_topic`/`azurerm_eventgrid_domain`).
- Event Grid domain nuance: when publishing CloudEvents to a **domain endpoint**, Azure uses CloudEvent `source` as the target domain topic by default (unless domain input mapping is customized).
- Example guidance: for non-domain topics, `source` should be a stable producer identifier; for domain endpoints, `source` should be the domain topic selector.
- tfplugindocs reliability: generate docs through `go generate ./tools`, which now uses `tools/cmd/gendocs` to run from repository root. This avoids missing action docs seen with certain relative-path invocations.
- Action docs templates: keep explicit templates under `templates/actions/` so Example Usage is always rendered, instead of relying on auto-detected examples.
- Test/doc consistency: acceptance test error regexes should match implementation wording exactly to avoid false failures.
- Generated action docs can include nested block HTML anchors from `.SchemaMarkdown` (for example `<a id="nestedblock--..."></a>`), which trigger markdownlint `MD033` and adjacent heading spacing `MD022` in strict setups.
- For this repository, handle those generated schema lint conflicts with **template-scoped** markdownlint inline directives around `{{ .SchemaMarkdown | trimspace }}` in action templates, not by directly editing files under `docs/`.

## Authentication Reference

### Provider-level authentication

| Method                     | How to configure                                                                                                        |
| -------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| DefaultAzureCredential     | Omit `client_id`/`client_secret`/`tenant_id`; use Azure CLI, managed identity, workload identity, or `AZURE_*` env vars |
| Service Principal (secret) | Set all three of `client_id`, `client_secret`, `tenant_id` (provider block or env vars)                                 |
| Subscription               | `subscription_id` / `AZURE_SUBSCRIPTION_ID` / `ARM_SUBSCRIPTION_ID`                                                     |
| Azure DevOps org           | `organization_url` / `AZUREDEVOPS_ORG_URL`                                                                              |
| Cloud environment          | `environment` / `ARM_ENVIRONMENT` (`public` \| `usgovernment` \| `china`)                                               |

Credential selection is **binary** — if all three SPN fields are present, `ClientSecretCredential` is used exclusively. Otherwise, `DefaultAzureCredential` is used. There is no chaining or fallback between the two.

### Azure DevOps action-level auth

| Method                             | `auth_method` value          | Notes                                                                                                   |
| ---------------------------------- | ---------------------------- | ------------------------------------------------------------------------------------------------------- |
| Personal Access Token              | `"pat"`                      | Requires `personal_access_token` attribute                                                              |
| DefaultAzureCredential / SPN / DAC | `"default_azure_credential"` | Uses `p.Client.Credential.GetToken()` with DevOps scope `499b84ac-1321-427f-aa17-267ca6975798/.default` |
| _(backwards-compat alias)_         | `"service_principal"`        | Identical to `default_azure_credential`                                                                 |

## Key Dependencies

| Package                                              | Purpose                                            |
| ---------------------------------------------------- | -------------------------------------------------- |
| `github.com/hashicorp/terraform-plugin-framework`    | Terraform provider SDK (actions, schema, types)    |
| `github.com/hashicorp/go-azure-sdk/resource-manager` | Azure RM typed API clients (Automation)            |
| `github.com/hashicorp/go-azure-sdk/sdk`              | Auth interfaces, environments, HTTP                |
| `github.com/Azure/azure-sdk-for-go/sdk/azidentity`   | `DefaultAzureCredential`, `ClientSecretCredential` |
| `github.com/Azure/azure-sdk-for-go/sdk/azcore`       | `TokenCredential` interface, `policy`              |

## Resources

- [Terraform Plugin Framework docs](https://developer.hashicorp.com/terraform/plugin/framework)
- [Terraform Actions (1.14)](https://developer.hashicorp.com/terraform/language/actions)
- [Azure REST API reference](https://learn.microsoft.com/en-us/rest/api/azure/)
- [go-azure-sdk](https://github.com/hashicorp/go-azure-sdk)
- [Azure DevOps REST API](https://learn.microsoft.com/en-us/rest/api/azure/devops/)
- [azidentity docs](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/azidentity)

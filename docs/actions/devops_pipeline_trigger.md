---
page_title: "azureactions_devops_pipeline_trigger Action - azureactions"
subcategory: ""
description: |-
  Triggers an Azure DevOps pipeline run. Supports Personal Access Token (PAT) and service principal (Azure AD) authentication methods. Configure organization_url on the provider.
---

# azureactions_devops_pipeline_trigger (Action)

Triggers an Azure DevOps pipeline run. Supports Personal Access Token (PAT) and service principal (Azure AD) authentication methods. Configure `organization_url` on the provider.

## Example Usage

```terraform
terraform {
  required_version = ">= 1.14"
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }
  }
}

provider "azureactions" {
  organization_url = var.organization_url
}

variable "organization_url" {
  type        = string
  description = "Azure DevOps organization URL"
}

variable "project" {
  type        = string
  description = "Azure DevOps project name"
}

variable "pipeline_id" {
  type        = number
  description = "Azure DevOps pipeline ID"
}

variable "template_parameters" {
  type        = map(string)
  description = "Map of template parameters to pass when triggering the pipeline"
  default     = null
}

resource "terraform_data" "example" {
  input = timestamp()

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.azureactions_devops_pipeline_trigger.example]
    }
  }
}

action "azureactions_devops_pipeline_trigger" "example" {
  config {
    project             = var.project
    pipeline_id         = var.pipeline_id
    auth_method         = "default_azure_credential"
    wait_for_completion = true
    template_parameters = var.template_parameters
  }
}
```

## Retries and waiting

**Triggering the run.** Queuing a pipeline run is not idempotent, so the trigger request is only retried when Azure DevOps cannot have queued a run: the connection was never made (for example the connection was refused or the host name did not resolve), the request was throttled (HTTP 429, honouring `Retry-After`), or the access token was rejected (HTTP 401 with `default_azure_credential`: retried once after requesting the token again). It makes at most 3 attempts in total, including that retry. A 401 with a personal access token is not retried, because the same token would be sent again. A server error, or a timeout or lost connection after the request was sent, is not retried, because a run may already have been queued: the error says so and names the pipeline, so check the pipeline's runs before applying again. The same warning is given when Azure DevOps accepted the request but its response could not be read. If Microsoft Entra ID rejects the credential while the token is requested again, the trigger is not retried.

**Waiting for completion.** With `wait_for_completion = true` the action polls the run until it completes:

- Every request uses a current access token. With `default_azure_credential` the token is reused until 5 minutes before it expires and then replaced, so a wait longer than the token's lifetime keeps working. A 401 during polling is retried once after requesting the token again. The identity library may return the same token (it cannot be told to skip its cache), so a 401 directly after that retry fails the action. With a personal access token a 401 fails the action straight away.
- If Microsoft Entra ID rejects the credential's token request, the action fails straight away: HTTP 401 or 403, or HTTP 400 with an OAuth error such as `invalid_client` (for example an expired client secret), `unauthorized_client`, `invalid_grant` (for example revoked consent), `invalid_request` or `invalid_scope`, or with an error code the provider does not recognise. HTTP 400 with `temporarily_unavailable` or `server_error`, and any other failure to get a token (a network problem reaching Entra ID or the managed identity endpoint, an Azure CLI failure such as an expired sign-in, or a token request that takes longer than a minute), is retried like a transient error, but after 5 polls in a row that could not get a token the wait stops, so the action does not keep waiting until `timeout_minutes` runs out.
- Transient poll errors (network errors, a request that times out, HTTP 408, 429, 500, 502, 503, 504) are retried with exponential backoff and jitter, starting at the poll interval and capped at 2 minutes. A `Retry-After` header is honoured (up to 5 minutes). Each retry is reported as a progress message.
- Any other error (for example HTTP 400, 403 or 404, or a response that cannot be parsed) fails the action straight away.
- `timeout_minutes` starts once the run has been queued, so time the run spends waiting for an agent counts against it. When it runs out, the action fails but the pipeline run is not cancelled: the error gives the run's link, says the run may still be running and, when the last polls failed, gives the last poll error and how many polls in a row failed. Every other error that stops the wait also says the run was not cancelled and gives its link.

Each request for an access token, including the one before the run is triggered, times out after one minute, so a credential that does not answer cannot hold the action. Error and progress messages include at most the first 512 bytes of an Azure DevOps error response or of a credential error. Requests honour the `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` environment variables.

<!-- markdownlint-disable MD022 MD033 -->
<!-- action schema generated by tfplugindocs -->
## Schema

### Required

- `auth_method` (String) Authentication method to use. Accepted values: `pat` (Personal Access Token) or `default_azure_credential` (reuses the provider-level Azure credential chain). `service_principal` is retained as a backwards-compatible alias for `default_azure_credential`.
- `pipeline_id` (Number) The integer ID of the pipeline to trigger.
- `project` (String) The name or ID of the Azure DevOps project.

### Optional

- `branch_ref` (String) Git ref to run the pipeline against, e.g. `refs/heads/main`. When omitted the pipeline's default branch is used.
- `personal_access_token` (String) Personal Access Token (PAT) used when `auth_method` is `"pat"`. Must have Build (Read & execute) permission. Provide this value via a Terraform sensitive variable or the `TF_VAR_PERSONAL_ACCESS_TOKEN` environment variable to avoid it appearing in plan output.
- `stages_to_skip` (List of String) List of stage names to skip in the pipeline run.
- `template_parameters` (Map of String) Template parameter values. Keys are parameter names, values are parameter values.
- `timeout_minutes` (Number) Maximum time in minutes to wait for pipeline completion. Only used when wait_for_completion is true. Defaults to 30. The time starts once the run has been queued, so time the run spends waiting for an agent counts against it. When it runs out the action fails, but the pipeline run is not cancelled and may still be running.
- `variables` (Map of String) Pipeline variable overrides. Keys are variable names, values are variable values.
- `wait_for_completion` (Boolean) Whether to wait for the pipeline run to reach a terminal state before returning. Defaults to false. While waiting, each status poll uses a current access token, and transient poll errors (network errors, timeouts, HTTP 408, 429, 500, 502, 503, 504) are retried with backoff until `timeout_minutes` runs out.
<!-- markdownlint-enable MD022 MD033 -->

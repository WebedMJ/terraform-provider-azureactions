# Examples

This directory contains example Terraform configurations demonstrating usage of the Azure Actions provider.

## Provider Configuration

See [provider/provider.tf](provider/provider.tf) for basic provider setup.

## Actions

### Azure Automation Runbook Trigger

The [automation_runbook_trigger/action.tf](actions/automation_runbook_trigger/action.tf) example demonstrates how to configure the `azureactions_automation_runbook_trigger` action to execute an Azure Automation runbook.

### Azure DevOps Pipeline Trigger

The [devops_pipeline_trigger/action.tf](actions/devops_pipeline_trigger/action.tf) example demonstrates how to configure the `azureactions_devops_pipeline_trigger` action to trigger an Azure DevOps pipeline run using Personal Access Token authentication.

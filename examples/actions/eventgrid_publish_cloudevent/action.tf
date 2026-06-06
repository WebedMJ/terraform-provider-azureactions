terraform {
  required_version = ">= 1.14"
  required_providers {
    azureactions = {
      source = "WebedMJ/azureactions"
    }

    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

provider "azureactions" {
  subscription_id = var.subscription_id
}

provider "azurerm" {
  subscription_id = var.subscription_id

  features {}
}

variable "subscription_id" {
  type = string
}

variable "order_id" {
  type        = string
  description = "Sample order id to include in event payload"
}

data "azurerm_client_config" "current" {
}

resource "azurerm_resource_group" "example" {
  name     = "example-resources"
  location = "West Europe"
}

resource "azurerm_eventgrid_topic" "example" {
  name                = "my-eventgrid-topic"
  location            = azurerm_resource_group.example.location
  resource_group_name = azurerm_resource_group.example.name
  input_schema        = "CloudEventSchemaV1_0"

  tags = {
    environment = "Production"
  }
}

resource "azurerm_role_assignment" "eventgrid_data_sender" {
  scope                = azurerm_eventgrid_topic.example.id
  role_definition_name = "EventGrid Data Sender"
  principal_id         = data.azurerm_client_config.current.object_id
}

resource "terraform_data" "example" {
  input = var.order_id

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.azureactions_eventgrid_publish_cloudevent.example]
    }
  }

  depends_on = [azurerm_role_assignment.eventgrid_data_sender]
}

action "azureactions_eventgrid_publish_cloudevent" "example" {
  config {
    endpoint_url = azurerm_eventgrid_topic.example.endpoint

    cloud_event {
      source          = "example/orders"
      type            = "example.order.updated"
      subject         = "orders/${var.order_id}"
      time            = timestamp()
      datacontenttype = "application/json"
      data = {
        orderId = var.order_id
        status  = "processed"
      }
      cloud_event_extensions = {
        environment = "dev"
      }
    }
  }
}

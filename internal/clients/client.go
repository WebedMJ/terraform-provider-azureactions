// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package clients

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/hashicorp/go-azure-sdk/sdk/auth"
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
)

type Client struct {
	Account     Account
	Config      Config
	Environment *environments.Environment
	Authorizer  auth.Authorizer
	Credential  azcore.TokenCredential
}

type Account struct {
	SubscriptionID string
	TenantID       string
	ClientID       string
	Environment    string
}

type Config struct {
	SubscriptionID string
	TenantID       string
	ClientID       string
	ClientSecret   string
	Environment    string
}

func NewClient(ctx context.Context, config Config) (*Client, error) {
	environment, err := environments.FromName(config.Environment)
	if err != nil {
		return nil, err
	}

	credential, err := newTokenCredential(config)
	if err != nil {
		return nil, err
	}

	// Create authorizer for Azure Resource Manager using the shared token credential.
	authorizer, err := NewTokenCredentialAuthorizer(credential, environment.ResourceManager)
	if err != nil {
		return nil, err
	}

	return &Client{
		Account: Account{
			SubscriptionID: config.SubscriptionID,
			TenantID:       config.TenantID,
			ClientID:       config.ClientID,
			Environment:    config.Environment,
		},
		Config:      config,
		Environment: environment,
		Authorizer:  authorizer,
		Credential:  credential,
	}, nil
}

func (c *Client) AuthorizerFor(api environments.Api) (auth.Authorizer, error) {
	if c == nil {
		return nil, fmt.Errorf("client is nil")
	}
	if c.Credential == nil {
		return nil, fmt.Errorf("token credential is not configured")
	}

	return NewTokenCredentialAuthorizer(c.Credential, api)
}

func newTokenCredential(config Config) (azcore.TokenCredential, error) {
	clientOptions, err := azureClientOptions(config.Environment)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(config.TenantID) != "" && strings.TrimSpace(config.ClientID) != "" && strings.TrimSpace(config.ClientSecret) != "" {
		return azidentity.NewClientSecretCredential(
			config.TenantID,
			config.ClientID,
			config.ClientSecret,
			&azidentity.ClientSecretCredentialOptions{ClientOptions: clientOptions},
		)
	}

	options := &azidentity.DefaultAzureCredentialOptions{ClientOptions: clientOptions}
	if strings.TrimSpace(config.TenantID) != "" {
		options.TenantID = config.TenantID
	}

	return azidentity.NewDefaultAzureCredential(options)
}

func azureClientOptions(environmentName string) (azcore.ClientOptions, error) {
	switch strings.ToLower(strings.TrimSpace(environmentName)) {
	case "", "public":
		return azcore.ClientOptions{Cloud: cloud.AzurePublic}, nil
	case "usgovernment":
		return azcore.ClientOptions{Cloud: cloud.AzureGovernment}, nil
	case "china":
		return azcore.ClientOptions{Cloud: cloud.AzureChina}, nil
	default:
		return azcore.ClientOptions{}, fmt.Errorf("unsupported Azure environment %q", environmentName)
	}
}

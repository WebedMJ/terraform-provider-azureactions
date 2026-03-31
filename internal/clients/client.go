// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package clients

import (
	"context"

	"github.com/hashicorp/go-azure-sdk/sdk/auth"
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
)

type Client struct {
	Account     Account
	Config      Config
	Environment *environments.Environment
	Authorizer  auth.Authorizer
}

type Account struct {
	SubscriptionId string
	TenantId       string
	ClientId       string
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

	// Create authorizer for Azure Resource Manager
	authorizer, err := auth.NewAuthorizerFromCredentials(ctx, auth.Credentials{
		Environment:  *environment,
		TenantID:     config.TenantID,
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
	}, environment.ResourceManager)
	if err != nil {
		return nil, err
	}

	return &Client{
		Account: Account{
			SubscriptionId: config.SubscriptionID,
			TenantId:       config.TenantID,
			ClientId:       config.ClientID,
			Environment:    config.Environment,
		},
		Config:      config,
		Environment: environment,
		Authorizer:  authorizer,
	}, nil
}

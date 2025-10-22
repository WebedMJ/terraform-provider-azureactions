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

	authorizer, err := auth.NewAuthorizerFromCredentials(ctx, auth.Credentials{
		Environment:  *environment,
		TenantID:     config.TenantID,
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
	}, environment.MicrosoftGraph)
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
		Environment: environment,
		Authorizer:  authorizer,
	}, nil
}

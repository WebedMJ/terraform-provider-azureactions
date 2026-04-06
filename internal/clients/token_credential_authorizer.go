// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package clients

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/hashicorp/go-azure-sdk/sdk/auth"
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
	"golang.org/x/oauth2"
)

var _ auth.Authorizer = (*tokenCredentialAuthorizer)(nil)

type tokenCredentialAuthorizer struct {
	credential azcore.TokenCredential
	scopes     []string
}

func NewTokenCredentialAuthorizer(credential azcore.TokenCredential, api environments.Api) (auth.Authorizer, error) {
	if credential == nil {
		return nil, fmt.Errorf("token credential is nil")
	}

	scope, err := environments.Scope(api)
	if err != nil {
		return nil, fmt.Errorf("determining scope for %q: %w", api.Name(), err)
	}

	return &tokenCredentialAuthorizer{
		credential: credential,
		scopes:     []string{*scope},
	}, nil
}

func (a *tokenCredentialAuthorizer) Token(ctx context.Context, _ *http.Request) (*oauth2.Token, error) {
	if a == nil || a.credential == nil {
		return nil, fmt.Errorf("token credential authorizer is not configured")
	}

	token, err := a.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: a.scopes})
	if err != nil {
		return nil, err
	}

	return &oauth2.Token{
		AccessToken: token.Token,
		Expiry:      token.ExpiresOn,
		TokenType:   "Bearer",
	}, nil
}

func (a *tokenCredentialAuthorizer) AuxiliaryTokens(_ context.Context, _ *http.Request) ([]*oauth2.Token, error) {
	return nil, nil
}

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package clients

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
)

// testTokenCredential is a minimal azcore.TokenCredential for unit tests.
type testTokenCredential struct {
	token string
	err   error
}

func (m *testTokenCredential) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if m.err != nil {
		return azcore.AccessToken{}, m.err
	}
	return azcore.AccessToken{
		Token:     m.token,
		ExpiresOn: time.Now().Add(time.Hour),
	}, nil
}

var _ azcore.TokenCredential = (*testTokenCredential)(nil)

// testAPI returns an environments.Api backed by the public ARM endpoint.
func testAPI() environments.Api {
	return environments.ResourceManagerAPI("https://management.azure.com")
}

// TestNewTokenCredentialAuthorizer_NilCredential verifies that passing nil
// returns an error rather than panicking.
func TestNewTokenCredentialAuthorizer_NilCredential(t *testing.T) {
	t.Parallel()

	_, err := NewTokenCredentialAuthorizer(nil, testAPI())
	if err == nil {
		t.Error("expected error for nil credential, got nil")
	}
}

// TestTokenCredentialAuthorizer_Token verifies that Token returns a Bearer
// oauth2.Token obtained from the underlying azcore.TokenCredential.
func TestTokenCredentialAuthorizer_Token(t *testing.T) {
	t.Parallel()

	cred := &testTokenCredential{token: "test-access-token"}
	authorizer, err := NewTokenCredentialAuthorizer(cred, testAPI())
	if err != nil {
		t.Fatalf("unexpected error creating authorizer: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://management.azure.com/", nil)
	token, err := authorizer.Token(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error getting token: %v", err)
	}
	if token.AccessToken != "test-access-token" {
		t.Errorf("expected AccessToken %q, got %q", "test-access-token", token.AccessToken)
	}
	if token.TokenType != "Bearer" {
		t.Errorf("expected TokenType %q, got %q", "Bearer", token.TokenType)
	}
}

// TestTokenCredentialAuthorizer_Token_CredentialError verifies that an error
// from the underlying credential is propagated correctly.
func TestTokenCredentialAuthorizer_Token_CredentialError(t *testing.T) {
	t.Parallel()

	cred := &testTokenCredential{err: fmt.Errorf("credential unavailable")}
	authorizer, err := NewTokenCredentialAuthorizer(cred, testAPI())
	if err != nil {
		t.Fatalf("unexpected error creating authorizer: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://management.azure.com/", nil)
	_, err = authorizer.Token(context.Background(), req)
	if err == nil {
		t.Error("expected error from failing credential, got nil")
	}
}

// TestTokenCredentialAuthorizer_AuxiliaryTokens verifies that AuxiliaryTokens
// returns nil, nil — cross-tenant auth is not required by this provider.
func TestTokenCredentialAuthorizer_AuxiliaryTokens(t *testing.T) {
	t.Parallel()

	cred := &testTokenCredential{token: "test-token"}
	authorizer, err := NewTokenCredentialAuthorizer(cred, testAPI())
	if err != nil {
		t.Fatalf("unexpected error creating authorizer: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://management.azure.com/", nil)
	tokens, err := authorizer.AuxiliaryTokens(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokens != nil {
		t.Errorf("expected nil auxiliary tokens, got %v", tokens)
	}
}

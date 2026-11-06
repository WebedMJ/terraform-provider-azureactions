// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package devops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// tokenRefreshMargin is how long before expiry a cached Entra token is
// replaced. It keeps a token from expiring between being read and the request
// reaching Azure DevOps.
const tokenRefreshMargin = 5 * time.Minute

// authHeaderSource provides the Authorization header for each Azure DevOps
// request. It is asked again for every request (the trigger and every status
// poll), so a long wait never runs on an expired token.
type authHeaderSource interface {
	// Header returns the Authorization header value. forceRefresh asks for a
	// new token instead of a cached one, after Azure DevOps answered 401.
	Header(ctx context.Context, forceRefresh bool) (string, error)
}

// staticAuthHeader is a fixed header, used for Personal Access Tokens.
type staticAuthHeader string

func (s staticAuthHeader) Header(_ context.Context, _ bool) (string, error) {
	return string(s), nil
}

// credentialAuthHeader gets Entra tokens for Azure DevOps from a token
// credential. The token is cached until tokenRefreshMargin before its expiry,
// so polling every few seconds does not request a token every time (some
// credentials, such as the Azure CLI one, start a process per request).
type credentialAuthHeader struct {
	credential azcore.TokenCredential
	// now is used in tests to control the clock. A nil value uses time.Now.
	now func() time.Time

	mu    sync.Mutex
	token azcore.AccessToken
}

func (c *credentialAuthHeader) Header(ctx context.Context, forceRefresh bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now
	if c.now != nil {
		now = c.now
	}

	// A token without a known expiry is never reused.
	reusable := c.token.Token != "" && !c.token.ExpiresOn.IsZero() &&
		c.token.ExpiresOn.Sub(now()) > tokenRefreshMargin
	if forceRefresh || !reusable {
		// Always request the Azure DevOps token using the well-known app scope.
		token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{devOpsTokenScope}})
		if err != nil {
			return "", fmt.Errorf("obtaining Azure DevOps token for scope %q: %w", devOpsTokenScope, err)
		}
		if strings.TrimSpace(token.Token) == "" {
			return "", fmt.Errorf("obtaining Azure DevOps token for scope %q returned an empty access token", devOpsTokenScope)
		}
		c.token = token
	}

	return "Bearer " + c.token.Token, nil
}

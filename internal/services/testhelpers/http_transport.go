// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package testhelpers

import (
	"net/http"
	"strings"
)

// HostRewriteTransport rewrites outgoing request host to an httptest server.
type HostRewriteTransport struct {
	Host string
}

func (t *HostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = t.Host
	clone.Host = t.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// ServerHost returns host:port from an httptest server URL.
func ServerHost(rawURL string) string {
	const prefix = "http://"
	return strings.TrimPrefix(rawURL, prefix)
}

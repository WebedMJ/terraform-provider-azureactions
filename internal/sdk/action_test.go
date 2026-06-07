// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package sdk

import (
	"context"
	"fmt"
	"testing"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/clients"
	"github.com/hashicorp/terraform-plugin-framework/action"
)

// TestSetResponseErrorDiagnostic_StringDetail verifies that a string detail is
// set verbatim as the diagnostic message.
func TestSetResponseErrorDiagnostic_StringDetail(t *testing.T) {
	t.Parallel()

	resp := &action.InvokeResponse{
		SendProgress: func(action.InvokeProgressEvent) {},
	}
	SetResponseErrorDiagnostic(resp, "test summary", "test detail")

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic, got none")
	}
	errs := resp.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("expected 1 error diagnostic, got %d", len(errs))
	}
	if errs[0].Summary() != "test summary" {
		t.Errorf("expected summary %q, got %q", "test summary", errs[0].Summary())
	}
	if errs[0].Detail() != "test detail" {
		t.Errorf("expected detail %q, got %q", "test detail", errs[0].Detail())
	}
}

// TestSetResponseErrorDiagnostic_ErrorDetail verifies that an error value uses
// its Error() string as the diagnostic detail.
func TestSetResponseErrorDiagnostic_ErrorDetail(t *testing.T) {
	t.Parallel()

	resp := &action.InvokeResponse{
		SendProgress: func(action.InvokeProgressEvent) {},
	}
	err := fmt.Errorf("root: %w", fmt.Errorf("cause"))
	SetResponseErrorDiagnostic(resp, "error summary", err)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic, got none")
	}
	if resp.Diagnostics.Errors()[0].Detail() != err.Error() {
		t.Errorf("expected detail %q, got %q", err.Error(), resp.Diagnostics.Errors()[0].Detail())
	}
}

// TestSetResponseErrorDiagnostic_OtherDetail verifies that non-string/error
// detail values are formatted via fmt.Sprintf("%v", ...).
func TestSetResponseErrorDiagnostic_OtherDetail(t *testing.T) {
	t.Parallel()

	resp := &action.InvokeResponse{
		SendProgress: func(action.InvokeProgressEvent) {},
	}
	SetResponseErrorDiagnostic(resp, "summary", 42)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic, got none")
	}
	if resp.Diagnostics.Errors()[0].Detail() != "42" {
		t.Errorf("expected detail %q, got %q", "42", resp.Diagnostics.Errors()[0].Detail())
	}
}

// TestActionMetadata_Defaults_ValidClient verifies that Defaults populates
// Client and SubscriptionID from a valid provider client.
func TestActionMetadata_Defaults_ValidClient(t *testing.T) {
	t.Parallel()

	meta := &ActionMetadata{}
	client := &clients.Client{
		Account: clients.Account{SubscriptionID: "sub-abc"},
	}
	req := action.ConfigureRequest{ProviderData: client}
	resp := &action.ConfigureResponse{}
	meta.Defaults(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if meta.Client != client {
		t.Error("expected Client to be set to the provided client")
	}
	if meta.SubscriptionID != "sub-abc" {
		t.Errorf("expected SubscriptionID %q, got %q", "sub-abc", meta.SubscriptionID)
	}
}

// TestActionMetadata_Defaults_NilProviderData verifies that nil ProviderData
// is treated as a no-op (provider not yet configured).
func TestActionMetadata_Defaults_NilProviderData(t *testing.T) {
	t.Parallel()

	meta := &ActionMetadata{}
	req := action.ConfigureRequest{ProviderData: nil}
	resp := &action.ConfigureResponse{}
	meta.Defaults(context.Background(), req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}
	if meta.Client != nil {
		t.Error("expected Client to remain nil")
	}
	if meta.SubscriptionID != "" {
		t.Errorf("expected empty SubscriptionID, got %q", meta.SubscriptionID)
	}
}

// TestActionMetadata_Defaults_InvalidProviderData verifies that a wrong
// ProviderData type surfaces an error diagnostic and does not set Client.
func TestActionMetadata_Defaults_InvalidProviderData(t *testing.T) {
	t.Parallel()

	meta := &ActionMetadata{}
	req := action.ConfigureRequest{ProviderData: "not-a-client"}
	resp := &action.ConfigureResponse{}
	meta.Defaults(context.Background(), req, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected error diagnostic for invalid provider data, got none")
	}
	if meta.Client != nil {
		t.Error("expected Client to remain nil after type-assertion failure")
	}
}

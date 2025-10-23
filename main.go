// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"flag"
	"log"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/provider"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-go/tfprotov5/tf5server"
)

func main() {
	var debugMode bool

	// remove date and time stamp from log output as the plugin SDK already adds its own
	log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))

	flag.BoolVar(&debugMode, "debuggable", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	var serveOpts []tf5server.ServeOpt

	if debugMode {
		serveOpts = append(serveOpts, tf5server.WithManagedDebug())
	}

	err := tf5server.Serve("registry.terraform.io/WebedMJ/azureactions", func() tfprotov5.ProviderServer {
		providerServer, err := provider.NewFrameworkV5Provider(context.Background())
		if err != nil {
			log.Fatalf("creating Azure Actions Provider Server: %+v", err)
		}
		return providerServer
	}, serveOpts...)
	if err != nil {
		log.Fatal(err)
	}
}

// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"flag"
	"log"

	"github.com/WebedMJ/terraform-provider-azureactions/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var (
	version string = "dev"
)

func main() {
	var debugMode bool

	// remove date and time stamp from log output as the plugin SDK already adds its own
	log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))

	flag.BoolVar(&debugMode, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/WebedMJ/azureactions",
		Debug:   debugMode,
	}

	err := providerserver.Serve(context.Background(), provider.New(version), opts)
	if err != nil {
		log.Fatal(err.Error())
	}
}

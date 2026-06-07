// Copyright (c) WebedMJ
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	if err := os.Chdir(".."); err != nil {
		fmt.Fprintf(os.Stderr, "failed to change directory to repository root: %v\n", err)
		os.Exit(1)
	}

	args := []string{
		"run",
		"github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.24.0",
		"generate",
		"--provider-dir", ".",
		"--provider-name", "azureactions",
		"--website-source-dir", "./templates",
		"--rendered-website-dir", "./docs",
	}

	cmd := exec.Command("go", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to generate docs: %v\n", err)
		os.Exit(1)
	}
}

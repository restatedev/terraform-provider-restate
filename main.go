// Command terraform-provider-restate is the Terraform provider plugin for Restate Cloud.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/restatedev/terraform-provider-restate/internal/provider"
)

// version and commit are stamped at build time with -ldflags. They appear in the provider's user
// agent, which is how a request in a server-side log is traced back to a specific build.
var (
	version = "dev"
	commit  = ""
)

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider in debug mode, for attaching a debugger")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version, commit), providerserver.ServeOpts{
		Address: "registry.terraform.io/restatedev/restate",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}

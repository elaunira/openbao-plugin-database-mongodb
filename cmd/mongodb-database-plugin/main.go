// Copyright (c) 2024 Elaunira
// SPDX-License-Identifier: MPL-2.0

// Package main provides the entrypoint for the MongoDB database plugin.
package main

import (
	mongodb "github.com/elaunira/openbao-plugin-database-mongodb"
	"github.com/openbao/openbao/sdk/v2/database/dbplugin/v5"
)

var (
	version = "dev"
)

func main() {
	Run()
}

// Run instantiates a MongoDB object and runs the RPC server for the plugin.
func Run() {
	f := mongodb.New(mongodb.DefaultUserNameTemplate(), version)

	dbplugin.ServeMultiplex(f)
}

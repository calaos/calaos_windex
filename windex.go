//go:build go1.21

package main

import (
	"log"
	"os"

	"github.com/calaos/calaos_windex/cmd"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:    "Windex HTTP file index",
		Usage:   "List, serve and track file download",
		Version: "2.0",
		Commands: []*cli.Command{
			&cmd.CmdServe,
		},
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

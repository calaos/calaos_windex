// +build go1.6

package main

import (
	"os"
	"runtime"

	"github.com/calaos/calaos_windex/cmd"
	"github.com/urfave/cli"
)

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	app := cli.NewApp()
	app.Name = "Windex HTTP file index"
	app.Usage = "List, serve and track file download"
	app.Version = "2.0"
	app.Commands = []cli.Command{
		cmd.CmdServe,
	}
	app.Flags = append(app.Flags, []cli.Flag{}...)
	app.Run(os.Args)
}

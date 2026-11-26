// Command tasks is an example API built with Gonestly: projects with tasks, a token for
// writes, transactions across modules and a database that migrates itself.
package main

import (
	"context"
	"fmt"
	"os"

	_ "modernc.org/sqlite"

	"github.com/yaboylino/gonestly"

	"github.com/yaboylino/gonestly/examples/tasks/internal/app"
)

func main() {
	application, err := gonestly.New(app.Module)
	if err == nil {
		err = application.Run(context.Background())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

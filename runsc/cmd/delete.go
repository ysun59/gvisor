// Copyright 2018 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/google/subcommands"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/runsc/config"
	"gvisor.dev/gvisor/runsc/container"
	"gvisor.dev/gvisor/runsc/flag"

	"time"
)

// Delete implements subcommands.Command for the "delete" command.
type Delete struct {
	// force indicates that the container should be terminated if running.
	force bool
}

// Name implements subcommands.Command.Name.
func (*Delete) Name() string {
	timeUnixus:=time.Now().UnixNano() / 1e3   //us微秒
	fmt.Printf("%v us, 'DeleteName 1 delete.go'\n", timeUnixus)
	return "delete"
}

// Synopsis implements subcommands.Command.Synopsis.
func (*Delete) Synopsis() string {
	timeUnixus:=time.Now().UnixNano() / 1e3   //us微秒
	fmt.Printf("%v us, 'DeleteSynopsis 2 delete.go'\n", timeUnixus)
	return "delete resources held by a container"
}

// Usage implements subcommands.Command.Usage.
func (*Delete) Usage() string {
	timeUnixus:=time.Now().UnixNano() / 1e3   //us微秒
	fmt.Printf("%v us, 'DeleteUsage 3 delete.go'\n", timeUnixus)
	return `delete [flags] <container ids>`
}

// SetFlags implements subcommands.Command.SetFlags.
func (d *Delete) SetFlags(f *flag.FlagSet) {
	timeUnixus:=time.Now().UnixNano() / 1e3   //us微秒
	fmt.Printf("%v us, 'DeleteSetFlags 4 delete.go'\n", timeUnixus)
	f.BoolVar(&d.force, "force", false, "terminate container if running")
}

// Execute implements subcommands.Command.Execute.
func (d *Delete) Execute(_ context.Context, f *flag.FlagSet, args ...interface{}) subcommands.ExitStatus {
	timeUnixus:=time.Now().UnixNano() / 1e3   //us微秒
	fmt.Printf("%v us, 'DeleteExecute 5 delete.go'\n", timeUnixus)
	if f.NArg() == 0 {
		f.Usage()
		return subcommands.ExitUsageError
	}

	conf := args[0].(*config.Config)
	if err := d.execute(f.Args(), conf); err != nil {
		Fatalf("%v", err)
	}
	return subcommands.ExitSuccess
}

func (d *Delete) execute(ids []string, conf *config.Config) error {
	timeUnixus:=time.Now().UnixNano() / 1e3   //us微秒
	fmt.Printf("%v us, 'Deleteexecute 6 delete.go'\n", timeUnixus)
	for _, id := range ids {
		c, err := container.Load(conf.RootDir, container.FullID{ContainerID: id}, container.LoadOpts{})
		if err != nil {
			if os.IsNotExist(err) && d.force {
				log.Warningf("couldn't find container %q: %v", id, err)
				return nil
			}
			return fmt.Errorf("loading container %q: %v", id, err)
		}
		if !d.force && c.Status != container.Created && c.Status != container.Stopped {
			return fmt.Errorf("cannot delete container that is not stopped without --force flag")
		}
		if err := c.Destroy(); err != nil {
			return fmt.Errorf("destroying container: %v", err)
		}
	}
	return nil
}

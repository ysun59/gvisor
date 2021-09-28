// Copyright 2020 The gVisor Authors.
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

// Binary runsc implements the OCI runtime interface.
package main

import (
	"gvisor.dev/gvisor/runsc/cli"
	"time"
	"fmt"
)

func main() {
	timeUnixus:=time.Now().UnixNano() / 1e3   //us微秒
	fmt.Printf("%v us, 'enter runsc main.go'\n", timeUnixus)
	cli.Main(version)
	timeUnixus =time.Now().UnixNano() / 1e3   //us
	fmt.Printf("%v us, 'runsc main.go end'\n", timeUnixus)
}

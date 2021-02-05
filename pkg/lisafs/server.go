// Copyright 2021 The gVisor Authors.
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

package lisafs

import "gvisor.dev/gvisor/pkg/sync"

// Server serves a filesystem tree. A server may be shared across various
// connections if they all mount to the same mouth path. Provides utilities
// to safely modify the filesystem tree.
type Server struct {
	// renameMu synchronizes rename operations within this filesystem tree.
	renameMu sync.RWMutex

	// mountPath represents the absolute path at which this server is mounted.
	// mountPath is immutable. Note that this is called 'attachPath' in p9.
	mountPath string
}

// WithRenameRLock invokes fn with the server's rename mutex locked for
// reading. This ensures that no rename operations are occurring while fn
// executes (assuming rename correctly locks the rename mutex for writing).
func (s *Server) WithRenameRLock(fn func() error) error {
	s.renameMu.RLock()
	err := fn()
	s.renameMu.RUnlock()
	return err
}

// WithRenameLock invokes fn with the server's rename mutex locked for writing.
// fn must be doing a rename operation (relocating a filesystem node).
func (s *Server) WithRenameLock(fn func() error) error {
	s.renameMu.Lock()
	err := fn()
	s.renameMu.Unlock()
	return err
}

// MountPath returns the host path at which this connection is mounted.
// This path is absolute and path.Clean()'d.
func (s *Server) MountPath() string {
	return s.mountPath
}

// ServerImpl contains the implementation details for a Server.
// Implementations of ServerImpl should contain their associated Server by
// pointer because servers can be shared across ServerImpls.
type ServerImpl interface {
	// Mount is called when a Mount RPC is made. It initializes the ServerImpl
	// and mounts it at server.MountPath().
	Mount(c *Connection, server *Server) (*Inode, error)

	// Handlers returns a list of RPC handlers which can be indexed by the
	// handler's corresponding MID. Note that there are 2 special messages for
	// which handlers do not have to be provided: Error and Mount. If
	// handlers[MID] is nil, then that MID is not supported.
	Handlers() []RPCHandler

	// Server returns the associated Server. This server might be shared across
	// connections. This helps when we have bind mounts that are shared between
	// containers in a runsc pod. This is initialized via Mount().
	Server() *Server

	// MaxMessageSize is the maximum payload length (in bytes) that can be sent
	// to this server implementation.
	MaxMessageSize() uint32
}

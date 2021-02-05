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

import (
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/flipcall"
	"gvisor.dev/gvisor/pkg/log"
	"gvisor.dev/gvisor/pkg/p9"
	"gvisor.dev/gvisor/pkg/sync"
	"gvisor.dev/gvisor/pkg/unet"
)

// ConnectionManager manages the lifetime (from creation to destruction) of
// connections in the gofer process. It is also responsible for server sharing.
type ConnectionManager struct {
	// servers contains a mapping between a server and the path at which it was
	// mounted. The path must be filepath.Clean()'d. It is protected by serversMu.
	serversMu sync.Mutex
	servers   map[string]*Server

	// connWg counts the number of active connections being tracked.
	connWg sync.WaitGroup
}

// StartConnection starts the connection on a separate goroutine and tracks it.
func (cm *ConnectionManager) StartConnection(c *Connection) {
	cm.connWg.Add(1)
	go func() {
		c.Run()
		cm.connWg.Done()
	}()
}

// Preconditions: mountPath must be filepath.Clean()'d.
func (cm *ConnectionManager) getServer(mountPath string) *Server {
	var s *Server
	cm.serversMu.Lock()
	defer cm.serversMu.Unlock()
	if cm.servers == nil {
		cm.servers = make(map[string]*Server)
	}
	if s = cm.servers[mountPath]; s == nil {
		s = &Server{
			mountPath: mountPath,
		}
		cm.servers[mountPath] = s
	}
	return s
}

// CreateConnection initializes a new connection - creating a server if
// required. The connection must be started separately.
func (cm *ConnectionManager) CreateConnection(sock *unet.Socket, serverImpl ServerImpl) (*Connection, error) {
	c := &Connection{
		cm:         cm,
		sockComm:   newSockComm(sock),
		serverImpl: serverImpl,
		channels:   make([]*channel, 0, maxChannels()),
		fds:        make(map[FDID]FD),
		nextFDID:   InvalidFDID + 1,
	}

	alloc, err := flipcall.NewPacketWindowAllocator()
	if err != nil {
		return nil, err
	}
	c.channelAlloc = alloc
	return c, nil
}

// Wait waits for all connections to terminate.
func (cm *ConnectionManager) Wait() {
	cm.connWg.Wait()
}

// Connection represents a connection between a gofer mount in the sentry and
// the gofer process. This is owned by the gofer process and facilitates
// communication with the Client.
type Connection struct {
	// cm is the ConnectionManager that owns this connection.
	cm *ConnectionManager

	// serverImpl serves a filesystem tree that this connection is immutably
	// associated with.
	serverImpl ServerImpl

	// mounted is a one way flag indicating whether this connection has been
	// mounted correctly and the server is initialized properly.
	mounted bool

	// sockComm is the main socket by which this connections is established.
	sockComm *sockCommunicator

	// channelsMu protects channels.
	channelsMu sync.Mutex
	// channels keeps track of all open channels.
	channels []*channel

	// activeWg represents active channels.
	activeWg sync.WaitGroup

	// reqGate counts requests that are still being handled.
	reqGate sync.Gate

	// channelAlloc is used to allocate memory for channels.
	channelAlloc *flipcall.PacketWindowAllocator

	fdsMu sync.RWMutex
	// fds keeps tracks of open FDs on this server. It is protected by fdsMu.
	fds map[FDID]FD
	// nextFDID is the next available FDID. It is protected by fdsMu.
	nextFDID FDID
}

// ServerImpl returns the associated server implementation.
func (c *Connection) ServerImpl() ServerImpl {
	return c.serverImpl
}

// Run defines the lifecycle of a connection.
func (c *Connection) Run() {
	defer c.close()

	// Start handling requests on this connection.
	for {
		m, payloadLen, err := c.sockComm.rcvMsg(0 /* wantFDs */)
		if err != nil {
			log.Debugf("sock read failed, closing connection: %v", err)
			return
		}

		respM, respPayloadLen, respFDs := c.handleMsg(c.sockComm, m, payloadLen)
		err = c.sockComm.sndPrepopulatedMsg(respM, respPayloadLen, respFDs)
		closeFDs(respFDs)
		if err != nil {
			log.Debugf("sock write failed, closing connection: %v", err)
			return
		}
	}
}

// service starts servicing the passed channel until the channel is shutdown.
// This is a blocking method and hence must be called in a separate goroutine.
func (c *Connection) service(ch *channel) error {
	rcvDataLen, err := ch.data.RecvFirst()
	if err != nil {
		return err
	}
	for rcvDataLen > 0 {
		m, payloadLen, err := ch.rcvMsg(rcvDataLen)
		if err != nil {
			return err
		}
		respM, respPayloadLen, respFDs := c.handleMsg(ch, m, payloadLen)
		numFDs := ch.sendFDs(respFDs)
		closeFDs(respFDs)

		ch.marshalHdr(respM, numFDs)
		rcvDataLen, err = ch.data.SendRecv(respPayloadLen + chanHeaderLen)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Connection) respondError(comm Communicator, err unix.Errno) (MID, uint32, []int) {
	resp := &ErrorResp{errno: uint32(err)}
	respLen := uint32(resp.SizeBytes())
	resp.MarshalUnsafe(comm.PayloadBuf(respLen))
	return Error, respLen, nil
}

func (c *Connection) handleMsg(comm Communicator, m MID, payloadLen uint32) (MID, uint32, []int) {
	if !c.reqGate.Enter() {
		// c.close() has been called; the connection is shutting down.
		return c.respondError(comm, unix.ECONNRESET)
	}
	defer c.reqGate.Leave()

	if !c.mounted && m != Mount {
		log.Warningf("connection must first be mounted")
		return c.respondError(comm, unix.EINVAL)
	}

	// Check if the message is supported.
	handlers := c.serverImpl.Handlers()
	if int(m) >= len(handlers) || handlers[m] == nil {
		log.Warningf("received request which is not supported by the server, MID = %d", m)
		return c.respondError(comm, unix.EOPNOTSUPP)
	}

	// Try handling the request.
	respPayloadLen, err := handlers[m](c, comm, payloadLen)
	fds := comm.ReleaseFDs()
	if err != nil {
		closeFDs(fds)
		return c.respondError(comm, p9.ExtractErrno(err))
	}

	return m, respPayloadLen, fds
}

func (c *Connection) close() {
	// Wait for completion of all inflight requests. This is mostly so that if
	// a request is stuck, the sandbox supervisor has the opportunity to kill
	// us with SIGABRT to get a stack dump of the offending handler.
	c.reqGate.Close()

	// Shutdown and clean up channels.
	c.channelsMu.Lock()
	for _, ch := range c.channels {
		ch.shutdown()
	}
	c.activeWg.Wait()
	for _, ch := range c.channels {
		ch.destroy()
	}
	// This is to prevent additional channels from being created.
	c.channels = nil
	c.channelsMu.Unlock()

	// Free the channel memory.
	if c.channelAlloc != nil {
		c.channelAlloc.Destroy()
	}

	// Ensure the connection is closed.
	c.sockComm.destroy()

	// Cleanup all FDs.
	c.fdsMu.Lock()
	for fdid := range c.fds {
		fd := c.removeFDLocked(fdid)
		fd.DecRef(nil) // Drop the ref held by c.
	}
	c.fdsMu.Unlock()
}

// LookupFD retrieves the FD identified by id on this connection. This
// operation increments the returned FD's ref which is owned by the caller.
func (c *Connection) LookupFD(id FDID) (FD, error) {
	c.fdsMu.RLock()
	defer c.fdsMu.RUnlock()

	fd, ok := c.fds[id]
	if !ok {
		return nil, unix.EBADF
	}
	fd.IncRef()
	return fd, nil
}

// InsertFD inserts the passed fd into the internal datastructure to track FDs.
// The caller must hold a ref on fd which is transferred to the connection.
func (c *Connection) InsertFD(fd FD) FDID {
	c.fdsMu.Lock()
	defer c.fdsMu.Unlock()

	res := c.nextFDID
	c.nextFDID++
	if c.nextFDID < res {
		panic("ran out of FDIDs")
	}
	c.fds[res] = fd
	return res
}

// RemoveFD makes c stop tracking the passed FDID and drops its ref on it.
func (c *Connection) RemoveFD(id FDID) {
	c.fdsMu.Lock()
	fd := c.removeFDLocked(id)
	c.fdsMu.Unlock()
	if fd != nil {
		// Drop the ref held by c. This can take arbitrarily long. So do not hold
		// c.fdsMu while calling it.
		fd.DecRef(nil)
	}
}

// removeFDLocked makes c stop tracking the passed FDID. Note that the caller
// must drop ref on the returned fd (preferably without holding c.fdsMu).
//
// Precondition: c.fdsMu is locked.
func (c *Connection) removeFDLocked(id FDID) FD {
	fd := c.fds[id]
	if fd == nil {
		log.Warningf("removeFDLocked called on non-existent FDID %d", id)
		return nil
	}
	delete(c.fds, id)
	return fd
}

// supportedMessages returns all message IDs that are supported on this
// connection. An MID is supported if handlers[MID] != nil.
func (c *Connection) supportedMessages() []MID {
	var res []MID
	for m, handler := range c.serverImpl.Handlers() {
		if handler != nil {
			res = append(res, MID(m))
		}
	}
	return res
}

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

// Package network provides facilities to support tcpip.Endpoints that operate
// at the network layer or above.
package network

import (
	"fmt"
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/sync"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport"
)

// Endpoint is a datagram-based endpoint. It only supports sending datagrams to
// a peer.
//
// +stateify savable
type Endpoint struct {
	// The following fields must only be set once then never changed.
	stack      *stack.Stack `state:"manual"`
	ops        *tcpip.SocketOptions
	netProto   tcpip.NetworkProtocolNumber
	transProto tcpip.TransportProtocolNumber

	// state holds a transport.DatagramBasedEndpointState.
	//
	// state must be read from/written to atomically.
	state uint32

	// The following fields are protected by mu.
	mu   sync.RWMutex `state:"nosave"`
	info stack.TransportEndpointInfo
	// owner is the owner of transmitted packets.
	owner                tcpip.PacketOwner
	writeShutdown        bool
	effectiveNetProto    tcpip.NetworkProtocolNumber
	connectedRoute       *stack.Route `state:"manual"`
	multicastMemberships map[multicastMembership]struct{}
	// TODO(https://gvisor.dev/issue/6389): Use different fields for IPv4/IPv6.
	ttl uint8
	// TODO(https://gvisor.dev/issue/6389): Use different fields for IPv4/IPv6.
	multicastTTL uint8
	// TODO(https://gvisor.dev/issue/6389): Use different fields for IPv4/IPv6.
	multicastAddr tcpip.Address
	// TODO(https://gvisor.dev/issue/6389): Use different fields for IPv4/IPv6.
	multicastNICID tcpip.NICID
	// sendTOS represents IPv4 TOS or IPv6 TrafficClass,
	// applied while sending packets. Defaults to 0 as on Linux.
	// TODO(https://gvisor.dev/issue/6389): Use different fields for IPv4/IPv6.
	sendTOS uint8
}

// +stateify savable
type multicastMembership struct {
	nicID         tcpip.NICID
	multicastAddr tcpip.Address
}

// Init initializes the endpoint.
func (e *Endpoint) Init(s *stack.Stack, netProto tcpip.NetworkProtocolNumber, transProto tcpip.TransportProtocolNumber, ops *tcpip.SocketOptions) {
	if e.multicastMemberships != nil {
		panic(fmt.Sprintf("endpoint is already initialized; got e.multicastMemberships = %#v, want = nil", e.multicastMemberships))
	}

	switch netProto {
	case header.IPv4ProtocolNumber, header.IPv6ProtocolNumber:
	default:
		panic(fmt.Sprintf("invalid protocol number = %d", netProto))
	}

	*e = Endpoint{
		stack:      s,
		ops:        ops,
		netProto:   netProto,
		transProto: transProto,

		state: uint32(transport.DatagramEndpointStateInitial),

		info: stack.TransportEndpointInfo{
			NetProto:   netProto,
			TransProto: transProto,
		},
		effectiveNetProto: netProto,
		// Linux defaults to TTL=1.
		multicastTTL:         1,
		multicastMemberships: make(map[multicastMembership]struct{}),
	}
}

// NetProto returns the network protocol the endpoint was initialized with.
func (e *Endpoint) NetProto() tcpip.NetworkProtocolNumber {
	return e.netProto
}

// setState sets the state of the endpoint.
func (e *Endpoint) setEndpointState(state transport.DatagramEndpointState) {
	atomic.StoreUint32(&e.state, uint32(state))
}

// State returns the state of the endpoint.
func (e *Endpoint) State() transport.DatagramEndpointState {
	return transport.DatagramEndpointState(atomic.LoadUint32(&e.state))
}

// Close cleans the endpoint's resources and leaves the endpoint in a closed
// state.
func (e *Endpoint) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.State() == transport.DatagramEndpointStateClosed {
		return
	}

	for mem := range e.multicastMemberships {
		e.stack.LeaveGroup(e.netProto, mem.nicID, mem.multicastAddr)
	}
	e.multicastMemberships = nil

	if e.connectedRoute != nil {
		e.connectedRoute.Release()
		e.connectedRoute = nil
	}

	e.setEndpointState(transport.DatagramEndpointStateClosed)
}

// SetOwner sets the owner of transmitted packets.
func (e *Endpoint) SetOwner(owner tcpip.PacketOwner) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.owner = owner
}

func calculateTTL(route *stack.Route, ttl uint8, multicastTTL uint8) uint8 {
	if header.IsV4MulticastAddress(route.RemoteAddress()) || header.IsV6MulticastAddress(route.RemoteAddress()) {
		return multicastTTL
	}

	if ttl == 0 {
		return route.DefaultTTL()
	}

	return ttl
}

// WriteContext holds the context for a write.
type WriteContext struct {
	transProto tcpip.TransportProtocolNumber
	route      *stack.Route
	ttl        uint8
	tos        uint8
	owner      tcpip.PacketOwner
}

// Release releases held resources.
func (c *WriteContext) Release() {
	c.route.Release()
	*c = WriteContext{}
}

// WritePacketInfo is the properties of a packet that may be written.
type WritePacketInfo struct {
	NetProto                    tcpip.NetworkProtocolNumber
	LocalAddress, RemoteAddress tcpip.Address
	MaxHeaderLength             uint16
	RequiresTXTransportChecksum bool
}

// PacketInfo returns the properties of a packet that will be written.
func (c *WriteContext) PacketInfo() WritePacketInfo {
	return WritePacketInfo{
		NetProto:                    c.route.NetProto(),
		LocalAddress:                c.route.LocalAddress(),
		RemoteAddress:               c.route.RemoteAddress(),
		MaxHeaderLength:             c.route.MaxHeaderLength(),
		RequiresTXTransportChecksum: c.route.RequiresTXTransportChecksum(),
	}
}

// WritePacket attempts to write the packet.
func (c *WriteContext) WritePacket(pkt *stack.PacketBuffer, headerIncluded bool) tcpip.Error {
	pkt.Owner = c.owner

	if headerIncluded {
		return c.route.WriteHeaderIncludedPacket(pkt)
	}

	return c.route.WritePacket(stack.NetworkHeaderParams{
		Protocol: c.transProto,
		TTL:      c.ttl,
		TOS:      c.tos,
	}, pkt)
}

// AcquireContextForWrite acquires a WriteContext.
func (e *Endpoint) AcquireContextForWrite(opts tcpip.WriteOptions) (WriteContext, tcpip.Error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// MSG_MORE is unimplemented. This also means that MSG_EOR is a no-op.
	if opts.More {
		return WriteContext{}, &tcpip.ErrInvalidOptionValue{}
	}

	if e.State() == transport.DatagramEndpointStateClosed {
		return WriteContext{}, &tcpip.ErrInvalidEndpointState{}
	}

	if e.writeShutdown {
		return WriteContext{}, &tcpip.ErrClosedForSend{}
	}

	route := e.connectedRoute
	if opts.To == nil {
		// If the user doesn't specify a destination, they should have
		// connected to another address.
		if e.State() != transport.DatagramEndpointStateConnected {
			return WriteContext{}, &tcpip.ErrDestinationRequired{}
		}

		route.Acquire()
	} else {
		// Reject destination address if it goes through a different
		// NIC than the endpoint was bound to.
		nicID := opts.To.NIC
		if nicID == 0 {
			nicID = tcpip.NICID(e.ops.GetBindToDevice())
		}
		if e.info.BindNICID != 0 {
			if nicID != 0 && nicID != e.info.BindNICID {
				return WriteContext{}, &tcpip.ErrNoRoute{}
			}

			nicID = e.info.BindNICID
		}

		dst, netProto, err := e.checkV4MappedLocked(*opts.To)
		if err != nil {
			return WriteContext{}, err
		}

		route, _, err = e.connectRoute(nicID, dst, netProto)
		if err != nil {
			return WriteContext{}, err
		}
	}

	if !e.ops.GetBroadcast() && route.IsOutboundBroadcast() {
		route.Release()
		return WriteContext{}, &tcpip.ErrBroadcastDisabled{}
	}

	return WriteContext{
		transProto: e.transProto,
		route:      route,
		ttl:        calculateTTL(route, e.ttl, e.multicastTTL),
		tos:        e.sendTOS,
		owner:      e.owner,
	}, nil
}

// Disconnect disconnects the endpoint from its peer.
func (e *Endpoint) Disconnect() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.State() != transport.DatagramEndpointStateConnected {
		return
	}

	// Exclude ephemerally bound endpoints.
	if e.info.BindNICID != 0 || e.info.ID.LocalAddress == "" {
		e.info.ID = stack.TransportEndpointID{
			LocalAddress: e.info.ID.LocalAddress,
		}
		e.setEndpointState(transport.DatagramEndpointStateBound)
	} else {
		e.info.ID = stack.TransportEndpointID{}
		e.setEndpointState(transport.DatagramEndpointStateInitial)
	}

	e.connectedRoute.Release()
	e.connectedRoute = nil
}

// connectRoute establishes a route to the specified interface or the
// configured multicast interface if no interface is specified and the
// specified address is a multicast address.
func (e *Endpoint) connectRoute(nicID tcpip.NICID, addr tcpip.FullAddress, netProto tcpip.NetworkProtocolNumber) (*stack.Route, tcpip.NICID, tcpip.Error) {
	localAddr := e.info.ID.LocalAddress
	if e.isBroadcastOrMulticast(nicID, netProto, localAddr) {
		// A packet can only originate from a unicast address (i.e., an interface).
		localAddr = ""
	}

	if header.IsV4MulticastAddress(addr.Addr) || header.IsV6MulticastAddress(addr.Addr) {
		if nicID == 0 {
			nicID = e.multicastNICID
		}
		if localAddr == "" && nicID == 0 {
			localAddr = e.multicastAddr
		}
	}

	// Find a route to the desired destination.
	r, err := e.stack.FindRoute(nicID, localAddr, addr.Addr, netProto, e.ops.GetMulticastLoop())
	if err != nil {
		return nil, 0, err
	}
	return r, nicID, nil
}

// Connect connects the endpoint to the address.
func (e *Endpoint) Connect(addr tcpip.FullAddress) tcpip.Error {
	return e.ConnectAndThen(addr, func(_ tcpip.NetworkProtocolNumber, _, _ stack.TransportEndpointID) tcpip.Error {
		return nil
	})
}

// ConnectAndThen connects the endpoint to the address and then calls the
// provided function.
//
// If the function returns an error, the endpoint's state does not change. The
// function will be called with the network protocol used to connect to the peer
// and the source and destination addresses that will be used to send traffic to
// the peer.
func (e *Endpoint) ConnectAndThen(addr tcpip.FullAddress, f func(netProto tcpip.NetworkProtocolNumber, previousID, nextID stack.TransportEndpointID) tcpip.Error) tcpip.Error {
	addr.Port = 0

	e.mu.Lock()
	defer e.mu.Unlock()

	nicID := addr.NIC
	switch e.State() {
	case transport.DatagramEndpointStateInitial:
	case transport.DatagramEndpointStateBound, transport.DatagramEndpointStateConnected:
		if e.info.BindNICID == 0 {
			break
		}

		if nicID != 0 && nicID != e.info.BindNICID {
			return &tcpip.ErrInvalidEndpointState{}
		}

		nicID = e.info.BindNICID
	default:
		return &tcpip.ErrInvalidEndpointState{}
	}

	addr, netProto, err := e.checkV4MappedLocked(addr)
	if err != nil {
		return err
	}

	r, nicID, err := e.connectRoute(nicID, addr, netProto)
	if err != nil {
		return err
	}

	id := stack.TransportEndpointID{
		LocalAddress:  e.info.ID.LocalAddress,
		RemoteAddress: r.RemoteAddress(),
	}
	if e.State() == transport.DatagramEndpointStateInitial {
		id.LocalAddress = r.LocalAddress()
	}

	if err := f(r.NetProto(), e.info.ID, id); err != nil {
		return err
	}

	if e.connectedRoute != nil {
		// If the endpoint was previously connected then release any previous route.
		e.connectedRoute.Release()
	}
	e.connectedRoute = r
	e.info.ID = id
	e.info.RegisterNICID = nicID
	e.effectiveNetProto = netProto
	e.setEndpointState(transport.DatagramEndpointStateConnected)
	return nil
}

// Shutdown shutsdown the endpoint.
func (e *Endpoint) Shutdown() tcpip.Error {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch state := e.State(); state {
	case transport.DatagramEndpointStateInitial, transport.DatagramEndpointStateClosed:
		return &tcpip.ErrNotConnected{}
	case transport.DatagramEndpointStateBound, transport.DatagramEndpointStateConnected:
		e.writeShutdown = true
		return nil
	default:
		panic(fmt.Sprintf("unhandled state = %s", state))
	}
}

// checkV4MappedLocked determines the effective network protocol and converts
// addr to its canonical form.
func (e *Endpoint) checkV4MappedLocked(addr tcpip.FullAddress) (tcpip.FullAddress, tcpip.NetworkProtocolNumber, tcpip.Error) {
	unwrapped, netProto, err := e.info.AddrNetProtoLocked(addr, e.ops.GetV6Only())
	if err != nil {
		return tcpip.FullAddress{}, 0, err
	}
	return unwrapped, netProto, nil
}

func (e *Endpoint) isBroadcastOrMulticast(nicID tcpip.NICID, netProto tcpip.NetworkProtocolNumber, addr tcpip.Address) bool {
	return addr == header.IPv4Broadcast || header.IsV4MulticastAddress(addr) || header.IsV6MulticastAddress(addr) || e.stack.IsSubnetBroadcast(nicID, netProto, addr)
}

// Bind binds the endpoint to the address.
func (e *Endpoint) Bind(addr tcpip.FullAddress) tcpip.Error {
	return e.BindAndThen(addr, func(tcpip.NetworkProtocolNumber, tcpip.Address) tcpip.Error {
		return nil
	})
}

// BindAndThen binds the endpoint to the address and then calls the provided
// function.
//
// If the function returns an error, the endpoint's state does not change. The
// function will be called with the bound network protocol and address.
func (e *Endpoint) BindAndThen(addr tcpip.FullAddress, f func(tcpip.NetworkProtocolNumber, tcpip.Address) tcpip.Error) tcpip.Error {
	addr.Port = 0

	e.mu.Lock()
	defer e.mu.Unlock()

	// Don't allow binding once endpoint is not in the initial state
	// anymore.
	if e.State() != transport.DatagramEndpointStateInitial {
		return &tcpip.ErrInvalidEndpointState{}
	}

	addr, netProto, err := e.checkV4MappedLocked(addr)
	if err != nil {
		return err
	}

	nicID := addr.NIC
	if len(addr.Addr) != 0 && !e.isBroadcastOrMulticast(addr.NIC, netProto, addr.Addr) {
		nicID = e.stack.CheckLocalAddress(nicID, netProto, addr.Addr)
		if nicID == 0 {
			return &tcpip.ErrBadLocalAddress{}
		}
	}

	if err := f(netProto, addr.Addr); err != nil {
		return err
	}

	e.info.ID = stack.TransportEndpointID{
		LocalAddress: addr.Addr,
	}
	e.info.BindNICID = nicID
	e.info.RegisterNICID = nicID
	e.info.BindAddr = addr.Addr
	e.effectiveNetProto = netProto
	e.setEndpointState(transport.DatagramEndpointStateBound)
	return nil
}

// GetLocalAddress returns the address that the endpoint is bound to.
func (e *Endpoint) GetLocalAddress() tcpip.FullAddress {
	e.mu.RLock()
	defer e.mu.RUnlock()

	addr := e.info.BindAddr
	if e.State() == transport.DatagramEndpointStateConnected {
		addr = e.connectedRoute.LocalAddress()
	}

	return tcpip.FullAddress{
		NIC:  e.info.RegisterNICID,
		Addr: addr,
	}
}

// GetRemoteAddress returns the address that the endpoint is connected to.
func (e *Endpoint) GetRemoteAddress() (tcpip.FullAddress, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.State() != transport.DatagramEndpointStateConnected {
		return tcpip.FullAddress{}, false
	}

	return tcpip.FullAddress{
		Addr: e.connectedRoute.RemoteAddress(),
		NIC:  e.info.RegisterNICID,
	}, true
}

// SetSockOptInt sets the socket option.
func (e *Endpoint) SetSockOptInt(opt tcpip.SockOptInt, v int) tcpip.Error {
	switch opt {
	case tcpip.MTUDiscoverOption:
		// Return not supported if the value is not disabling path
		// MTU discovery.
		if v != tcpip.PMTUDiscoveryDont {
			return &tcpip.ErrNotSupported{}
		}

	case tcpip.MulticastTTLOption:
		e.mu.Lock()
		e.multicastTTL = uint8(v)
		e.mu.Unlock()

	case tcpip.TTLOption:
		e.mu.Lock()
		e.ttl = uint8(v)
		e.mu.Unlock()

	case tcpip.IPv4TOSOption:
		e.mu.Lock()
		e.sendTOS = uint8(v)
		e.mu.Unlock()

	case tcpip.IPv6TrafficClassOption:
		e.mu.Lock()
		e.sendTOS = uint8(v)
		e.mu.Unlock()
	}

	return nil
}

// GetSockOptInt returns the socket option.
func (e *Endpoint) GetSockOptInt(opt tcpip.SockOptInt) (int, tcpip.Error) {
	switch opt {
	case tcpip.MTUDiscoverOption:
		// The only supported setting is path MTU discovery disabled.
		return tcpip.PMTUDiscoveryDont, nil

	case tcpip.MulticastTTLOption:
		e.mu.Lock()
		v := int(e.multicastTTL)
		e.mu.Unlock()
		return v, nil

	case tcpip.TTLOption:
		e.mu.Lock()
		v := int(e.ttl)
		e.mu.Unlock()
		return v, nil

	case tcpip.IPv4TOSOption:
		e.mu.RLock()
		v := int(e.sendTOS)
		e.mu.RUnlock()
		return v, nil

	case tcpip.IPv6TrafficClassOption:
		e.mu.RLock()
		v := int(e.sendTOS)
		e.mu.RUnlock()
		return v, nil

	default:
		return -1, &tcpip.ErrUnknownProtocolOption{}
	}
}

// SetSockOpt sets the socket option.
func (e *Endpoint) SetSockOpt(opt tcpip.SettableSocketOption) tcpip.Error {
	switch v := opt.(type) {
	case *tcpip.MulticastInterfaceOption:
		e.mu.Lock()
		defer e.mu.Unlock()

		fa := tcpip.FullAddress{Addr: v.InterfaceAddr}
		fa, netProto, err := e.checkV4MappedLocked(fa)
		if err != nil {
			return err
		}
		nic := v.NIC
		addr := fa.Addr

		if nic == 0 && addr == "" {
			e.multicastAddr = ""
			e.multicastNICID = 0
			break
		}

		if nic != 0 {
			if !e.stack.CheckNIC(nic) {
				return &tcpip.ErrBadLocalAddress{}
			}
		} else {
			nic = e.stack.CheckLocalAddress(0, netProto, addr)
			if nic == 0 {
				return &tcpip.ErrBadLocalAddress{}
			}
		}

		if e.info.BindNICID != 0 && e.info.BindNICID != nic {
			return &tcpip.ErrInvalidEndpointState{}
		}

		e.multicastNICID = nic
		e.multicastAddr = addr

	case *tcpip.AddMembershipOption:
		if !header.IsV4MulticastAddress(v.MulticastAddr) && !header.IsV6MulticastAddress(v.MulticastAddr) {
			return &tcpip.ErrInvalidOptionValue{}
		}

		nicID := v.NIC

		if v.InterfaceAddr.Unspecified() {
			if nicID == 0 {
				if r, err := e.stack.FindRoute(0, "", v.MulticastAddr, e.netProto, false /* multicastLoop */); err == nil {
					nicID = r.NICID()
					r.Release()
				}
			}
		} else {
			nicID = e.stack.CheckLocalAddress(nicID, e.netProto, v.InterfaceAddr)
		}
		if nicID == 0 {
			return &tcpip.ErrUnknownDevice{}
		}

		memToInsert := multicastMembership{nicID: nicID, multicastAddr: v.MulticastAddr}

		e.mu.Lock()
		defer e.mu.Unlock()

		if _, ok := e.multicastMemberships[memToInsert]; ok {
			return &tcpip.ErrPortInUse{}
		}

		if err := e.stack.JoinGroup(e.netProto, nicID, v.MulticastAddr); err != nil {
			return err
		}

		e.multicastMemberships[memToInsert] = struct{}{}

	case *tcpip.RemoveMembershipOption:
		if !header.IsV4MulticastAddress(v.MulticastAddr) && !header.IsV6MulticastAddress(v.MulticastAddr) {
			return &tcpip.ErrInvalidOptionValue{}
		}

		nicID := v.NIC
		if v.InterfaceAddr.Unspecified() {
			if nicID == 0 {
				if r, err := e.stack.FindRoute(0, "", v.MulticastAddr, e.netProto, false /* multicastLoop */); err == nil {
					nicID = r.NICID()
					r.Release()
				}
			}
		} else {
			nicID = e.stack.CheckLocalAddress(nicID, e.netProto, v.InterfaceAddr)
		}
		if nicID == 0 {
			return &tcpip.ErrUnknownDevice{}
		}

		memToRemove := multicastMembership{nicID: nicID, multicastAddr: v.MulticastAddr}

		e.mu.Lock()
		defer e.mu.Unlock()

		if _, ok := e.multicastMemberships[memToRemove]; !ok {
			return &tcpip.ErrBadLocalAddress{}
		}

		if err := e.stack.LeaveGroup(e.netProto, nicID, v.MulticastAddr); err != nil {
			return err
		}

		delete(e.multicastMemberships, memToRemove)

	case *tcpip.SocketDetachFilterOption:
		return nil
	}
	return nil
}

// GetSockOpt returns the socket option.
func (e *Endpoint) GetSockOpt(opt tcpip.GettableSocketOption) tcpip.Error {
	switch o := opt.(type) {
	case *tcpip.MulticastInterfaceOption:
		e.mu.Lock()
		*o = tcpip.MulticastInterfaceOption{
			NIC:           e.multicastNICID,
			InterfaceAddr: e.multicastAddr,
		}
		e.mu.Unlock()

	default:
		return &tcpip.ErrUnknownProtocolOption{}
	}
	return nil
}

// Info returns a copy of the endpoint info.
func (e *Endpoint) Info() stack.TransportEndpointInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.info
}

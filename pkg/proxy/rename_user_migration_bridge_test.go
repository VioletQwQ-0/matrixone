// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proxy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/matrixorigin/matrixone/pkg/pb/query"
	"github.com/matrixorigin/matrixone/pkg/pb/status"
	queryclient "github.com/matrixorigin/matrixone/pkg/queryservice/client"
)

// These helpers are compiled only into proxy tests. They let the external
// integration test use the real Server and tunnel without a production hook.
type ExportMigrationSource struct {
	Address              string
	ConnID               uint32
	AccountID            uint32
	UserID               uint32
	PrincipalExported    bool
	LastInsertIDExported bool
	Err                  error
}

type ExportMigrationTarget struct {
	Address              string
	ConnID               uint32
	AccountID            uint32
	UserID               uint32
	PrincipalExported    bool
	LastInsertIDExported bool
	Success              bool
	Err                  error
}

type ExportMigrationProbe struct {
	Source       chan ExportMigrationSource
	BeforeTarget chan ExportMigrationTarget
	AfterTarget  chan ExportMigrationTarget
	connID       atomic.Uint32
	resume       chan struct{}
	resumeOnce   sync.Once
}

func (p *ExportMigrationProbe) SetConnID(id uint32) { p.connID.Store(id) }
func (p *ExportMigrationProbe) Release()            { p.resumeOnce.Do(func() { close(p.resume) }) }

type exportMigrationQueryClient struct {
	queryclient.QueryClient
	probe *ExportMigrationProbe
}

func (c *exportMigrationQueryClient) SendMessage(ctx context.Context, address string, req *query.Request) (*query.Response, error) {
	id := c.probe.connID.Load()
	if id != 0 && req.CmdMethod == query.CmdMethod_MigrateConnFrom && req.MigrateConnFromRequest != nil && req.MigrateConnFromRequest.ConnID == id {
		resp, err := c.QueryClient.SendMessage(ctx, address, req)
		event := ExportMigrationSource{Address: address, ConnID: id, Err: err}
		if resp != nil && resp.MigrateConnFromResponse != nil {
			r := resp.MigrateConnFromResponse
			event.AccountID, event.UserID = r.SourceAccountID, r.SourceUserID
			event.PrincipalExported, event.LastInsertIDExported = r.SourcePrincipalExported, r.LastInsertIDExported
		}
		select {
		case c.probe.Source <- event:
		default:
		}
		return resp, err
	}
	if id != 0 && req.CmdMethod == query.CmdMethod_MigrateConnTo && req.MigrateConnToRequest != nil && req.MigrateConnToRequest.ConnID == id {
		r := req.MigrateConnToRequest
		event := ExportMigrationTarget{
			Address: address, ConnID: id, AccountID: r.SourceAccountID, UserID: r.SourceUserID,
			PrincipalExported: r.SourcePrincipalExported, LastInsertIDExported: r.LastInsertIDExported,
		}
		select {
		case c.probe.BeforeTarget <- event:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-c.probe.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		resp, err := c.QueryClient.SendMessage(ctx, address, req)
		event.Err = err
		event.Success = resp != nil && resp.MigrateConnToResponse != nil && resp.MigrateConnToResponse.Success
		select {
		case c.probe.AfterTarget <- event:
		default:
		}
		return resp, err
	}
	return c.QueryClient.SendMessage(ctx, address, req)
}

// ExportInstallMigrationProbe must run before Start, before any client can
// capture the handler's query client. The wrapper forwards the actual RPCs.
func ExportInstallMigrationProbe(s *Server) (*ExportMigrationProbe, error) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.started || s.closed || s.handler == nil || s.handler.queryClient == nil || s.handler.connCache != nil {
		return nil, fmt.Errorf("proxy must be unstarted with connection cache disabled")
	}
	p := &ExportMigrationProbe{
		Source: make(chan ExportMigrationSource, 1), BeforeTarget: make(chan ExportMigrationTarget, 1),
		AfterTarget: make(chan ExportMigrationTarget, 1), resume: make(chan struct{}),
	}
	s.handler.queryClient = &exportMigrationQueryClient{QueryClient: s.handler.queryClient, probe: p}
	return p, nil
}

func ExportProxyAddress(s *Server) string { return s.listener.Addr().String() }

func exportTunnelConnID(tun *tunnel) uint32 {
	if tun == nil {
		return 0
	}
	tun.mu.Lock()
	defer tun.mu.Unlock()
	if tun.cc == nil {
		return 0
	}
	return tun.cc.ConnID()
}

func ExportConnectionCN(s *Server, connID uint32) (string, error) {
	type entry struct {
		cnID string
		tun  *tunnel
	}
	m := s.handler.rebalancer.connManager
	m.Lock()
	var entries []entry
	for cnID, tunnels := range m.cnTunnels {
		for tun := range tunnels {
			entries = append(entries, entry{cnID: cnID, tun: tun})
		}
	}
	m.Unlock()
	found := ""
	for _, e := range entries {
		if exportTunnelConnID(e.tun) == connID {
			if found != "" && found != e.cnID {
				return "", fmt.Errorf("connection %d is registered on multiple CNs", connID)
			}
			found = e.cnID
		}
	}
	if found == "" {
		return "", fmt.Errorf("proxy connection %d is not registered", connID)
	}
	return found, nil
}

func ExportTransferConnection(ctx context.Context, s *Server, sourceCN string, connID uint32) error {
	for _, tun := range s.handler.rebalancer.connManager.getTunnelsByCNID(sourceCN) {
		if exportTunnelConnID(tun) == connID {
			if !tun.tryStartTransferAttempt() {
				return fmt.Errorf("connection %d already has a transfer in progress", connID)
			}
			return tun.transfer(ctx)
		}
	}
	return fmt.Errorf("connection %d has no tunnel on CN %s", connID, sourceCN)
}

// ExportQuerySession uses the actual CN query service; it copies the status
// before returning the RPC response to its pool.
func ExportQuerySession(ctx context.Context, s *Server, address string, connID uint32) (*status.Session, error) {
	qc := s.handler.queryClient
	req := qc.NewRequest(query.CmdMethod_ShowProcessList)
	req.ShowProcessListRequest = &query.ShowProcessListRequest{Tenant: "sys", SysTenant: true}
	resp, err := qc.SendMessage(ctx, address, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("missing process-list response")
	}
	defer qc.Release(resp)
	if resp.ShowProcessListResponse != nil {
		for _, session := range resp.ShowProcessListResponse.Sessions {
			if session != nil && session.ConnID == connID {
				copy := *session
				return &copy, nil
			}
		}
	}
	return nil, fmt.Errorf("connection %d is absent from the CN process list", connID)
}

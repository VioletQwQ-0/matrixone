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

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

type blockingDynamicChaosStopper struct {
	stopStartedOnce sync.Once
	stopStarted     chan struct{}
	releaseOnce     sync.Once
	release         chan struct{}
}

type replacingDynamicChaosStopper struct{}

func (s *replacingDynamicChaosStopper) Stop() error {
	dynamicCNMu.Lock()
	dynamicCNServicePIDs[0] = 101
	dynamicCNMu.Unlock()
	return nil
}

func newBlockingDynamicChaosStopper() *blockingDynamicChaosStopper {
	return &blockingDynamicChaosStopper{
		stopStarted: make(chan struct{}),
		release:     make(chan struct{}),
	}
}

func (s *blockingDynamicChaosStopper) Stop() error {
	s.stopStartedOnce.Do(func() { close(s.stopStarted) })
	<-s.release
	return nil
}

func (s *blockingDynamicChaosStopper) releaseStop() {
	s.releaseOnce.Do(func() { close(s.release) })
}

func TestDynamicShutdownUsesFinalPIDSnapshotAfterChaosQuiesces(t *testing.T) {
	setLaunchTestHooks(t)
	dynamicCNMu.Lock()
	dynamicCNServiceCommands = [][]string{{"mo-service", "-cfg", "cn.toml"}}
	dynamicCNServicePIDs = []int{100}
	dynamicChaosTester = &replacingDynamicChaosStopper{}
	dynamicCNMu.Unlock()

	type killedProcess struct {
		pid    int
		signal syscall.Signal
	}
	var killed []killedProcess
	dynamicKill = func(pid int, signal syscall.Signal) error {
		killed = append(killed, killedProcess{pid: pid, signal: signal})
		return nil
	}
	dynamicWaitProcess = func(pid int) error {
		require.Equal(t, 101, pid)
		return nil
	}

	require.NoError(t, stopAllDynamicCNServicesGracefully(context.Background()))
	require.Equal(t, []killedProcess{{pid: 101, signal: syscall.SIGTERM}}, killed)
	dynamicCNMu.RLock()
	defer dynamicCNMu.RUnlock()
	require.Zero(t, dynamicCNServicePIDs[0])
}

func TestDynamicShutdownRejectsHTTPStartAfterStopping(t *testing.T) {
	setLaunchTestHooks(t)
	oldMux := http.DefaultServeMux
	oldListenAddr := *httpListenAddr
	chaosStopper := newBlockingDynamicChaosStopper()
	t.Cleanup(func() {
		chaosStopper.releaseStop()
		http.DefaultServeMux = oldMux
		*httpListenAddr = oldListenAddr
	})
	http.DefaultServeMux = http.NewServeMux()
	*httpListenAddr = "127.0.0.1:bad"
	dynamicCNMu.Lock()
	dynamicCNServiceCommands = [][]string{{"mo-service", "-cfg", "cn.toml"}}
	dynamicCNServicePIDs = []int{0}
	dynamicChaosTester = chaosStopper
	dynamicCNMu.Unlock()

	forkCalls := 0
	dynamicForkExec = func(string, []string, *syscall.ProcAttr) (int, error) {
		forkCalls++
		return 101, nil
	}
	serverDone := make(chan struct{})
	dynamicListenAndServe = func(string, http.Handler) error {
		close(serverDone)
		return nil
	}
	require.NoError(t, startDynamicCtlHTTPServer(*httpListenAddr))
	<-serverDone

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- stopAllDynamicCNServicesGracefully(context.Background())
	}()
	<-chaosStopper.stopStarted

	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp := httptest.NewRecorder()
		http.DefaultServeMux.ServeHTTP(resp, req)
		return resp
	}
	require.Equal(t, errDynamicCNStopping.Error(), request("/dynamic/cn?cn=0&action=start").Body.String())
	require.Zero(t, forkCalls)
	chaosStopper.releaseStop()

	require.NoError(t, <-shutdownDone)
	dynamicCNMu.RLock()
	defer dynamicCNMu.RUnlock()
	require.Zero(t, dynamicCNServicePIDs[0])
}

func TestDynamicShutdownRejectsChaosRestartAfterStopping(t *testing.T) {
	setLaunchTestHooks(t)
	chaosStopper := newBlockingDynamicChaosStopper()
	t.Cleanup(chaosStopper.releaseStop)
	dynamicCNMu.Lock()
	dynamicCNServiceCommands = [][]string{{"mo-service", "-cfg", "cn.toml"}}
	dynamicCNServicePIDs = []int{0}
	dynamicChaosTester = chaosStopper
	dynamicCNMu.Unlock()

	forkCalls := 0
	dynamicForkExec = func(string, []string, *syscall.ProcAttr) (int, error) {
		forkCalls++
		return 101, nil
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- stopAllDynamicCNServicesGracefully(context.Background())
	}()
	<-chaosStopper.stopStarted

	// This is the exact cfg.Chaos.Restart.RestartFunc producer installed by
	// startDynamicCNServices.
	require.NoError(t, restartDynamicCNByIndex(0))
	require.ErrorIs(t, startDynamicCNByIndex(0), errDynamicCNStopping)
	require.Zero(t, forkCalls)
	chaosStopper.releaseStop()

	require.NoError(t, <-shutdownDone)
	dynamicCNMu.RLock()
	defer dynamicCNMu.RUnlock()
	require.Zero(t, dynamicCNServicePIDs[0])
}

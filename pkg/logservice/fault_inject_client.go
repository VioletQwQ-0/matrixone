// Copyright 2021 - 2022 Matrix Origin
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

package logservice

import (
	"context"
	"sync"
	"time"

	"github.com/matrixorigin/matrixone/pkg/common/moerr"
	pb "github.com/matrixorigin/matrixone/pkg/pb/logservice"
)

// FaultInject sends a fault injection command directly to a LogService process.
func FaultInject(
	ctx context.Context,
	sid string,
	address string,
	command string,
	parameter string,
) (string, error) {
	respPool := &sync.Pool{}
	respPool.New = func() interface{} {
		return &RPCResponse{pool: respPool}
	}
	cc, err := getRPCClient(
		ctx,
		sid,
		address,
		respPool,
		defaultMaxMessageSize,
		false,
		time.Second*10,
	)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = cc.Close()
	}()

	req := &RPCRequest{
		Request: pb.Request{
			Method: pb.FAULT_INJECT,
			FaultInjectRequest: &pb.FaultInjectRequest{
				Method:     command,
				Parameters: parameter,
			},
		},
	}
	future, err := cc.Send(ctx, address, req)
	if err != nil {
		return "", err
	}
	defer future.Close()
	msg, err := future.Get()
	if err != nil {
		return "", err
	}
	response, ok := msg.(*RPCResponse)
	if !ok {
		panic("unexpected response type")
	}
	defer response.Release()
	if err := toError(ctx, response.Response); err != nil {
		return "", err
	}
	if response.FaultInjectResponse == nil {
		return "", moerr.NewInternalError(ctx, "missing fault inject response")
	}
	return response.FaultInjectResponse.Resp, nil
}

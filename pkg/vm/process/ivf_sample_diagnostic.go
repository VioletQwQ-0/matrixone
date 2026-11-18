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

package process

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/matrixorigin/matrixone/pkg/logutil"
	"go.uber.org/zap"
)

const IVFDiagnosticLimit = 25000

type ivfSampleDiagnosticKey struct{}

type ivfDiagnosticEvent struct {
	name    string
	elapsed time.Duration
	fields  []zap.Field
}

// IVFSampleDiagnostic is owned by one internal training statement. Log the
// bounded sequence after execution has settled, outside the pipeline edge lock.
type IVFSampleDiagnostic struct {
	id     string
	start  time.Time
	mu     sync.Mutex
	events []ivfDiagnosticEvent
}

func BeginIVFSampleDiagnostic(proc *Process, sql string) *IVFSampleDiagnostic {
	d := &IVFSampleDiagnostic{id: uuid.NewString(), start: time.Now()}
	logutil.Info("ivf sample diagnostic start",
		zap.String("statement_id", d.id),
		zap.String("service", proc.GetService()),
		zap.String("txn_id", hex.EncodeToString(proc.GetTxnOperator().Txn().ID)),
		zap.String("sql", sql))
	return d
}

func (d *IVFSampleDiagnostic) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, ivfSampleDiagnosticKey{}, d)
}

func IVFSampleDiagnosticFrom(ctx context.Context) *IVFSampleDiagnostic {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(ivfSampleDiagnosticKey{}).(*IVFSampleDiagnostic)
	return d
}

func (d *IVFSampleDiagnostic) first(name string, fields ...zap.Field) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, event := range d.events {
		if event.name == name {
			return
		}
	}
	d.events = append(d.events, ivfDiagnosticEvent{name, time.Since(d.start), fields})
}

func (d *IVFSampleDiagnostic) LimitReached(proc *Process) {
	d.first("limit_reached", zap.Int("rows", IVFDiagnosticLimit), zap.String("scope", fmt.Sprintf("%p", proc)))
}

func (d *IVFSampleDiagnostic) LimitDelivered() {
	d.first("limit_delivered", zap.Int("rows", IVFDiagnosticLimit))
}

// IVFDiagnosticContext records the two errors before a scope can cancel its
// pipeline during cleanup.
func IVFDiagnosticContext(label string, ctx context.Context) []zap.Field {
	if ctx == nil {
		return []zap.Field{zap.Bool(label+"_missing", true)}
	}
	return []zap.Field{zap.String(label+"_err", fmt.Sprint(ctx.Err())), zap.String(label+"_cause", fmt.Sprint(context.Cause(ctx)))}
}

func (d *IVFSampleDiagnostic) ScopeError(phase string, proc *Process, edge *PipelineEdge, raw, retained error, normalized bool, state []zap.Field) {
	d.first("scope_retained_error", append(state,
		zap.String("phase", phase), zap.String("scope", fmt.Sprintf("%p", proc)),
		zap.String("edge", fmt.Sprintf("%p", edge)), zap.Bool("normalized", normalized),
		zap.String("raw_type", fmt.Sprintf("%T", raw)), zap.NamedError("raw_error", raw),
		zap.String("unwrap_type", fmt.Sprintf("%T", errors.Unwrap(raw))), zap.String("unwrap_error", fmt.Sprint(errors.Unwrap(raw))),
		zap.Bool("raw_is_canceled", errors.Is(raw, context.Canceled)),
		zap.Bool("raw_is_deadline", errors.Is(raw, context.DeadlineExceeded)),
		zap.String("retained_type", fmt.Sprintf("%T", retained)), zap.NamedError("retained_error", retained))...)
}

func (d *IVFSampleDiagnostic) fatalEdge(edge *PipelineEdge, signal PipelineSignal) {
	var caller [16]uintptr
	n := runtime.Callers(2, caller[:])
	d.first("edge_first_fatal", zap.String("edge", fmt.Sprintf("%p", edge)),
		zap.String("terminal", signal.EventType.String()), zap.NamedError("terminal_error", signal.TerminalErr()),
		zap.Int("channel_capacity", cap(edge.Ch2)), zap.Int("remaining_senders", edge.fatalRemaining),
		zap.Strings("callers", ivfDiagnosticCallers(caller[:n])))
}

func ivfDiagnosticCallers(pc []uintptr) []string {
	frames := runtime.CallersFrames(pc)
	var callers []string
	for {
		f, more := frames.Next()
		callers = append(callers, fmt.Sprintf("%s:%d", f.Function, f.Line))
		if !more {
			return callers
		}
	}
}

func (d *IVFSampleDiagnostic) queryCancel(ctx context.Context) {
	var pc [16]uintptr
	n := runtime.Callers(3, pc[:])
	d.first("query_cancel_called", append(IVFDiagnosticContext("query_before_cancel", ctx),
		zap.Strings("callers", ivfDiagnosticCallers(pc[:n])))...)
}

func (d *IVFSampleDiagnostic) Finish(err error) {
	d.mu.Lock()
	events := append([]ivfDiagnosticEvent(nil), d.events...)
	d.mu.Unlock()
	for i, event := range events {
		logutil.Info("ivf sample diagnostic event", append(event.fields,
			zap.String("statement_id", d.id), zap.String("event", event.name),
			zap.Int("sequence", i+1), zap.Duration("elapsed", event.elapsed))...)
	}
	logutil.Info("ivf sample diagnostic finish", zap.String("statement_id", d.id),
		zap.Int("events", len(events)), zap.Error(err))
}

func (e *PipelineEdge) SetIVFSampleDiagnostic(d *IVFSampleDiagnostic) {
	e.terminalMu.Lock()
	e.ivfSampleDiagnostic = d
	e.terminalMu.Unlock()
}

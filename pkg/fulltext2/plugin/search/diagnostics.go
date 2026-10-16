// Copyright 2026 Matrix Origin
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package search

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/matrixorigin/matrixone/pkg/fulltext2"
	"github.com/matrixorigin/matrixone/pkg/logutil"
	"go.uber.org/zap"
)

// Opt-in at process startup with at most four SHA256 hashes of MATCH payloads.
// Three receipts per payload bound logs and retained state even if a diagnostic
// client accidentally leaves its request loop running. No raw SQL is logged.
const searchDiagnosticsEnv = "MO_FT2_SEARCH_DIAGNOSTICS"

type diagnosticBudget struct {
	hash string
	used atomic.Uint32
}

type diagnosticGate struct {
	budgets []diagnosticBudget
}

var searchDiagnosticSelector = sync.OnceValue(func() *diagnosticSelector {
	return &diagnosticSelector{source: os.Getenv(searchDiagnosticsEnv)}
})

// @path allows a run-owned client to publish the four payload hashes after it
// has loaded and verified its parameter pack. Until that bounded file exists,
// no diagnostic is enabled. The first valid configuration is immutable for the
// lifetime of the process; there is no watcher, retry worker or reload protocol.
type diagnosticSelector struct {
	source string
	mu     sync.Mutex
	gate   atomic.Pointer[diagnosticGate]
}

func (s *diagnosticSelector) load() *diagnosticGate {
	if s.source == "" {
		return nil
	}
	if gate := s.gate.Load(); gate != nil {
		return gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if gate := s.gate.Load(); gate != nil {
		return gate
	}
	value := s.source
	if strings.HasPrefix(value, "@") {
		path := strings.TrimPrefix(value, "@")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 260 {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 261))
		if err != nil || len(data) > 260 {
			return nil
		}
		value = strings.TrimSpace(string(data))
	}
	gate := parseDiagnosticGate(value)
	if gate != nil {
		s.gate.Store(gate)
	}
	return gate
}

func parseDiagnosticGate(value string) *diagnosticGate {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > 4 {
		return nil
	}
	g := &diagnosticGate{budgets: make([]diagnosticBudget, 0, len(parts))}
	for _, part := range parts {
		b, err := hex.DecodeString(part)
		if err != nil || len(b) != sha256.Size {
			return nil
		}
		hash := hex.EncodeToString(b)
		for i := range g.budgets {
			if g.budgets[i].hash == hash {
				return nil
			}
		}
		g.budgets = append(g.budgets, diagnosticBudget{hash: hash})
	}
	return g
}

func (g *diagnosticGate) claim(payload []byte) (hash string, round uint32) {
	if g == nil {
		return "", 0
	}
	sum := sha256.Sum256(payload)
	hash = hex.EncodeToString(sum[:])
	for i := range g.budgets {
		budget := &g.budgets[i]
		if budget.hash != hash {
			continue
		}
		for n := budget.used.Load(); n < 3; n = budget.used.Load() {
			if budget.used.CompareAndSwap(n, n+1) {
				return hash, n + 1
			}
		}
		break
	}
	return "", 0
}

type searchDiagnosticReceipt struct {
	Version       int                          `json:"version"`
	QueryID       string                       `json:"query_id"`
	ScanSHA256    string                       `json:"scan_sha256"`
	PatternSHA256 string                       `json:"pattern_sha256"`
	IncludeSHA256 string                       `json:"include_sha256"`
	Sample        uint32                       `json:"sample"`
	PlannedLimit  uint64                       `json:"planned_limit"`
	RuntimeLimit  uint64                       `json:"runtime_limit"`
	Path          string                       `json:"path"`
	Status        string                       `json:"status"`
	Work          *fulltext2.SearchDiagnostics `json:"work,omitempty"`
}

func (u *reader) beginSearchDiagnostic(q *fulltext2.Fulltext2Query) *searchDiagnosticReceipt {
	return u.beginSearchDiagnosticWithGate(q, searchDiagnosticSelector().load())
}

func (u *reader) beginSearchDiagnosticWithGate(q *fulltext2.Fulltext2Query, gate *diagnosticGate) *searchDiagnosticReceipt {
	hash, sample := gate.claim(q.Pattern)
	if sample == 0 {
		return nil
	}
	// encoding/json sorts map keys; generated protobuf Marshal does not promise
	// a stable order for TableDef maps in an otherwise identical scan.
	encoded, err := json.Marshal(u.spec)
	if err != nil {
		return nil
	}
	scan := sha256.Sum256(encoded)
	include := sha256.Sum256(q.IncludePredsJSON)
	r := &searchDiagnosticReceipt{
		Version: 1, QueryID: u.proc.QueryId(), ScanSHA256: hex.EncodeToString(scan[:]),
		PatternSHA256: hash, Sample: sample, PlannedLimit: u.plannedLimit,
		IncludeSHA256: hex.EncodeToString(include[:]),
		RuntimeLimit:  u.limit, Path: "SearchInto", Status: "COMPLETE",
	}
	if u.limit == 0 {
		r.Path, r.Status = "stream", "INCONCLUSIVE"
	} else {
		r.Work = &fulltext2.SearchDiagnostics{}
		q.Diagnostics = r.Work
	}
	return r
}

func (r *searchDiagnosticReceipt) finish(err error, cancelled bool) {
	if r == nil {
		return
	}
	if err != nil {
		r.Status = "ERROR"
	} else if cancelled {
		r.Status = "CANCELLED"
	} else if r.QueryID == "" {
		r.Status = "INCONCLUSIVE"
	}
	logutil.Info("fulltext2 search diagnostic", zap.Any("receipt", r))
}

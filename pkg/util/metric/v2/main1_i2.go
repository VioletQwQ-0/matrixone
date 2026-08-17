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

package v2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Main1I2Template is deliberately fixed-cardinality. It is diagnostic data,
// never a user-controlled metric label or a production protocol field.
type Main1I2Template uint8

type Main1I2CommitStage uint8

const (
	Main1I2Unknown  Main1I2Template = 0
	Main1I2NewOrder Main1I2Template = 1
	Main1I2Payment  Main1I2Template = 2
	Main1I2Delivery Main1I2Template = 4
)

const (
	Main1I2PrePrepare Main1I2CommitStage = iota
	Main1I2PrepareTS
	Main1I2PrepareCommit
	Main1I2PreApplyCommit
	Main1I2PrepareWAL
	Main1I2WaitWalAndTail
	main1I2CommitStageCount
)

var main1I2CommitStageNames = [...]string{
	"pre-prepare",
	"prepare-ts",
	"prepare-commit",
	"pre-apply-commit",
	"prepare-wal",
	"wait-wal-and-tail",
}

const (
	main1I2MaxActive    = 4096
	main1I2MaxCompleted = 262144
	main1I2MaxTxnID     = 32
	Main1I2StageCount   = 12
)

var main1I2Enabled atomic.Bool
var main1I2CommitSubstageObservers [main1I2CommitStageCount][8]prometheus.Observer

type main1I2TxnKey struct {
	length uint8
	value  [main1I2MaxTxnID]byte
}

type main1I2ActiveTxn struct {
	hash     [16]byte
	template Main1I2Template
}

type Main1I2TxnRecord struct {
	TxnHash      string                   `json:"txn_hash"`
	Template     string                   `json:"template"`
	TemplateBits uint8                    `json:"template_bits"`
	StageNanos   [Main1I2StageCount]int64 `json:"stage_nanos"`
	CompletedNS  int64                    `json:"completed_ns"`
}

type main1I2Store struct {
	sync.RWMutex
	active    map[main1I2TxnKey]main1I2ActiveTxn
	completed []Main1I2TxnRecord
	next      uint64
	dropped   uint64
}

var main1I2 = main1I2Store{}

var (
	main1I2QueueDepthHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "queue_depth",
			Help:      "Pending profiled transactions at each commit queue.",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 15),
		}, []string{"queue"})
	main1I2QueueDepthGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "queue_depth_current",
			Help:      "Current pending profiled transactions at each commit queue.",
		}, []string{"queue"})
	main1I2QueueTxnCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "queue_txn_total",
			Help:      "Profiled transaction arrivals and services at each commit queue.",
		}, []string{"queue", "event"})
	main1I2CallbackDurationHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "callback_duration_seconds",
			Help:      "Callback busy duration for each commit queue.",
			Buckets:   getDurationBuckets(),
		}, []string{"queue"})
	main1I2CallbackIdleHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "callback_idle_seconds",
			Help:      "Time between profiled commit queue callbacks.",
			Buckets:   getDurationBuckets(),
		}, []string{"queue"})
	main1I2HandoffBatchHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "handoff_batch_size",
			Help:      "Profiled transactions handed off by each queue callback.",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 12),
		}, []string{"from", "to"})
	main1I2LogserviceGroupHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "logservice_group",
			Help:      "LogService durable group size, bytes, and append duration.",
			Buckets:   prometheus.ExponentialBuckets(1, 2, 24),
		}, []string{"measure"})
	main1I2CommitSubstageHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "commit_substage_seconds",
			Help:      "Commit service time by fixed substage and TPCC template.",
			Buckets:   getDurationBuckets(),
		}, []string{"stage", "template"})
	main1I2DroppedCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "mo",
			Subsystem: "main1_i2",
			Name:      "dropped_total",
			Help:      "I2 records dropped at a fixed capacity or invalid boundary.",
		}, []string{"reason"})
)

var (
	Main1I2PreWalDepth        = main1I2QueueDepthHistogram.WithLabelValues("prewal")
	Main1I2WalDepth           = main1I2QueueDepthHistogram.WithLabelValues("wal")
	Main1I2PreWalDepthCurrent = main1I2QueueDepthGauge.WithLabelValues("prewal")
	Main1I2WalDepthCurrent    = main1I2QueueDepthGauge.WithLabelValues("wal")
	Main1I2PreWalArrival      = main1I2QueueTxnCounter.WithLabelValues("prewal", "arrival")
	Main1I2PreWalService      = main1I2QueueTxnCounter.WithLabelValues("prewal", "service")
	Main1I2WalArrival         = main1I2QueueTxnCounter.WithLabelValues("wal", "arrival")
	Main1I2WalService         = main1I2QueueTxnCounter.WithLabelValues("wal", "service")
	Main1I2PreWalCallback     = main1I2CallbackDurationHistogram.WithLabelValues("prewal")
	Main1I2WalCallback        = main1I2CallbackDurationHistogram.WithLabelValues("wal")
	Main1I2PreWalIdle         = main1I2CallbackIdleHistogram.WithLabelValues("prewal")
	Main1I2WalIdle            = main1I2CallbackIdleHistogram.WithLabelValues("wal")
	Main1I2PreWalToWalBatch   = main1I2HandoffBatchHistogram.WithLabelValues("prewal", "wal")
	Main1I2LogserviceEntries  = main1I2LogserviceGroupHistogram.WithLabelValues("entries")
	Main1I2LogserviceBytes    = main1I2LogserviceGroupHistogram.WithLabelValues("bytes")
	Main1I2LogserviceSeconds  = main1I2LogserviceGroupHistogram.WithLabelValues("seconds")
)

func init() {
	for stage := Main1I2CommitStage(0); stage < main1I2CommitStageCount; stage++ {
		for template := Main1I2Template(0); template < 8; template++ {
			main1I2CommitSubstageObservers[stage][template] =
				main1I2CommitSubstageHistogram.WithLabelValues(
					main1I2CommitStageNames[stage], Main1I2TemplateName(template))
		}
	}
	main1I2Enabled.Store(os.Getenv("MO_MAIN1_I2") == "1")
	if main1I2Enabled.Load() {
		main1I2.active = make(map[main1I2TxnKey]main1I2ActiveTxn, main1I2MaxActive)
		main1I2.completed = make([]Main1I2TxnRecord, main1I2MaxCompleted)
	}
}

func initMain1I2Metrics() {
	registry.MustRegister(main1I2QueueDepthHistogram)
	registry.MustRegister(main1I2QueueDepthGauge)
	registry.MustRegister(main1I2QueueTxnCounter)
	registry.MustRegister(main1I2CallbackDurationHistogram)
	registry.MustRegister(main1I2CallbackIdleHistogram)
	registry.MustRegister(main1I2HandoffBatchHistogram)
	registry.MustRegister(main1I2LogserviceGroupHistogram)
	registry.MustRegister(main1I2CommitSubstageHistogram)
	registry.MustRegister(main1I2DroppedCounter)
}

func Main1I2Enabled() bool { return main1I2Enabled.Load() }

func Main1I2TemplateName(template Main1I2Template) string {
	switch template {
	case Main1I2NewOrder:
		return "new_order"
	case Main1I2Payment:
		return "payment"
	case Main1I2Delivery:
		return "delivery"
	default:
		if template == Main1I2Unknown {
			return "unknown"
		}
		return "mixed"
	}
}

// Main1I2ClassifyPreparedSQL runs once at PREPARE time and keeps neither SQL
// text nor unbounded template state. Multiple write sentinels are conservative.
func Main1I2ClassifyPreparedSQL(sql string) Main1I2Template {
	if !Main1I2Enabled() {
		return Main1I2Unknown
	}
	lower := strings.ToLower(sql)
	var found Main1I2Template
	for _, candidate := range []struct {
		sentinel string
		template Main1I2Template
	}{
		{"insert into bmsql_new_order", Main1I2NewOrder},
		{"insert into bmsql_history", Main1I2Payment},
		{"delete from bmsql_new_order", Main1I2Delivery},
	} {
		if !strings.Contains(lower, candidate.sentinel) {
			continue
		}
		found |= candidate.template
	}
	return found
}

func Main1I2MergeTemplate(current, next Main1I2Template) Main1I2Template {
	if next == Main1I2Unknown {
		return current
	}
	return current | next
}

func main1I2Key(txnID []byte) (main1I2TxnKey, bool) {
	var key main1I2TxnKey
	if len(txnID) == 0 || len(txnID) > len(key.value) {
		return key, false
	}
	key.length = uint8(len(txnID))
	copy(key.value[:], txnID)
	return key, true
}

func main1I2KeyString(txnID string) (main1I2TxnKey, bool) {
	var key main1I2TxnKey
	if len(txnID) == 0 || len(txnID) > len(key.value) {
		return key, false
	}
	key.length = uint8(len(txnID))
	copy(key.value[:], txnID)
	return key, true
}

func main1I2Hash(txnID []byte) [16]byte {
	full := sha256.Sum256(txnID)
	var hash [16]byte
	copy(hash[:], full[:len(hash)])
	return hash
}

// Main1I2RegisterFrontendTxn transfers ownership of a bounded template record
// to the TN completion path. Abort paths must call Main1I2AbortTxn.
func Main1I2RegisterFrontendTxn(txnID []byte, template Main1I2Template) bool {
	if !Main1I2Enabled() {
		return false
	}
	key, ok := main1I2Key(txnID)
	if !ok {
		main1I2DroppedCounter.WithLabelValues("invalid-txn-id").Inc()
		return false
	}
	main1I2.Lock()
	defer main1I2.Unlock()
	if _, exists := main1I2.active[key]; !exists && len(main1I2.active) >= main1I2MaxActive {
		main1I2.dropped++
		main1I2DroppedCounter.WithLabelValues("active-capacity").Inc()
		return false
	}
	main1I2.active[key] = main1I2ActiveTxn{hash: main1I2Hash(txnID), template: template}
	return true
}

func Main1I2LookupTemplate(txnID string) Main1I2Template {
	if !Main1I2Enabled() {
		return Main1I2Unknown
	}
	key, ok := main1I2KeyString(txnID)
	if !ok {
		return Main1I2Unknown
	}
	main1I2.RLock()
	active, found := main1I2.active[key]
	main1I2.RUnlock()
	if !found {
		return Main1I2Unknown
	}
	return active.template
}

func Main1I2ObserveCommitSubstage(stage Main1I2CommitStage, template Main1I2Template, duration time.Duration) {
	if !Main1I2Enabled() {
		return
	}
	if stage >= main1I2CommitStageCount || template >= 8 {
		main1I2DroppedCounter.WithLabelValues("invalid-substage").Inc()
		return
	}
	main1I2CommitSubstageObservers[stage][template].Observe(duration.Seconds())
}

func Main1I2AbortTxn(txnID []byte) {
	if !Main1I2Enabled() {
		return
	}
	key, ok := main1I2Key(txnID)
	if !ok {
		return
	}
	main1I2.Lock()
	delete(main1I2.active, key)
	main1I2.Unlock()
}

func Main1I2CompleteTN(txnID string, durations [Main1I2StageCount]time.Duration) {
	if !Main1I2Enabled() {
		return
	}
	key, ok := main1I2KeyString(txnID)
	if !ok {
		main1I2DroppedCounter.WithLabelValues("invalid-tn-txn-id").Inc()
		return
	}
	main1I2.Lock()
	defer main1I2.Unlock()
	active, frontendSeen := main1I2.active[key]
	delete(main1I2.active, key)
	if !frontendSeen {
		main1I2DroppedCounter.WithLabelValues("tn-without-frontend").Inc()
		return
	}
	record := Main1I2TxnRecord{
		TxnHash:      hex.EncodeToString(active.hash[:]),
		Template:     Main1I2TemplateName(active.template),
		TemplateBits: uint8(active.template),
		CompletedNS:  time.Now().UnixNano(),
	}
	for idx, duration := range durations {
		record.StageNanos[idx] = duration.Nanoseconds()
	}
	main1I2.completed[main1I2.next%uint64(len(main1I2.completed))] = record
	main1I2.next++
}

type main1I2Snapshot struct {
	Enabled         bool                    `json:"enabled"`
	Active          int                     `json:"active"`
	Completed       uint64                  `json:"completed"`
	Dropped         uint64                  `json:"dropped"`
	Capacity        uint64                  `json:"capacity"`
	StageNames      []string                `json:"stage_names"`
	FrontendRecords []main1I2FrontendRecord `json:"frontend_records"`
	TNRecords       []main1I2TNRecord       `json:"tn_records"`
}

type main1I2FrontendRecord struct {
	TxnHash      string `json:"txn_hash"`
	Template     string `json:"template"`
	TemplateBits uint8  `json:"template_bits"`
}

type main1I2TNRecord struct {
	TxnHash     string                   `json:"txn_hash"`
	StageNanos  [Main1I2StageCount]int64 `json:"stage_nanos"`
	CompletedNS int64                    `json:"completed_ns"`
}

func Main1I2SnapshotHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !Main1I2Enabled() {
			http.Error(w, "MAIN1 I2 disabled", http.StatusNotFound)
			return
		}
		main1I2.RLock()
		count := main1I2.next
		if count > uint64(len(main1I2.completed)) {
			count = uint64(len(main1I2.completed))
		}
		frontendRecords := make([]main1I2FrontendRecord, 0, count)
		tnRecords := make([]main1I2TNRecord, 0, count)
		start := main1I2.next - count
		for idx := uint64(0); idx < count; idx++ {
			record := main1I2.completed[(start+idx)%uint64(len(main1I2.completed))]
			frontendRecords = append(frontendRecords, main1I2FrontendRecord{
				TxnHash: record.TxnHash, Template: record.Template, TemplateBits: record.TemplateBits,
			})
			tnRecords = append(tnRecords, main1I2TNRecord{
				TxnHash: record.TxnHash, StageNanos: record.StageNanos, CompletedNS: record.CompletedNS,
			})
		}
		snapshot := main1I2Snapshot{
			Enabled:         true,
			Active:          len(main1I2.active),
			Completed:       main1I2.next,
			Dropped:         main1I2.dropped,
			Capacity:        uint64(len(main1I2.completed)),
			StageNames:      []string{"freeze", "prewal_queue", "prewal_work", "wal_queue", "wal_work", "apply_queue", "worker_queue", "table_wal_sync", "wal_durable", "tail_complete", "apply_commit", "done_apply"},
			FrontendRecords: frontendRecords,
			TNRecords:       tnRecords,
		}
		main1I2.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
}

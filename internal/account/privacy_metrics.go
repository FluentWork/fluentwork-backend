package account

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	privacyDeletes   atomic.Int64
	privacyUndeletes atomic.Int64
	tombstoneMu      sync.Mutex
	tombstonesByType = map[string]int64{}
)

func incPrivacyDelete()   { privacyDeletes.Add(1) }
func incPrivacyUndelete() { privacyUndeletes.Add(1) }

func incTombstone(entityType string) {
	if entityType == "" {
		entityType = "unknown"
	}
	tombstoneMu.Lock()
	tombstonesByType[entityType]++
	tombstoneMu.Unlock()
}

// PrivacyPrometheusMetrics renders A4 counters for GET /metrics.
func PrivacyPrometheusMetrics() string {
	var b strings.Builder
	b.WriteString("# HELP privacy_delete_total Times DELETE /account/data completed (including idempotent retries).\n")
	b.WriteString("# TYPE privacy_delete_total counter\n")
	fmt.Fprintf(&b, "privacy_delete_total %d\n", privacyDeletes.Load())
	b.WriteString("# HELP privacy_undelete_total Times support undelete-user succeeded.\n")
	b.WriteString("# TYPE privacy_undelete_total counter\n")
	fmt.Fprintf(&b, "privacy_undelete_total %d\n", privacyUndeletes.Load())
	b.WriteString("# HELP tombstone_inserted_total A4 tombstone rows inserted by entity_type.\n")
	b.WriteString("# TYPE tombstone_inserted_total counter\n")
	tombstoneMu.Lock()
	defer tombstoneMu.Unlock()
	if len(tombstonesByType) == 0 {
		b.WriteString("tombstone_inserted_total{entity_type=\"none\"} 0\n")
		return b.String()
	}
	entityTypes := make([]string, 0, len(tombstonesByType))
	for entityType := range tombstonesByType {
		entityTypes = append(entityTypes, entityType)
	}
	sort.Strings(entityTypes)
	for _, entityType := range entityTypes {
		fmt.Fprintf(&b, "tombstone_inserted_total{entity_type=%q} %d\n", entityType, tombstonesByType[entityType])
	}
	return b.String()
}

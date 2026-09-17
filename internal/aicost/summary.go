package aicost

import (
	"context"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// Supported roll-ups. The business question this answers is 归因与熔断
// (51_ §4.3): which task type, which model, which day is spending the audio
// seconds and tokens.
const (
	GroupByTaskType = "task_type"
	GroupByModel    = "model"
	GroupByDay      = "day"
)

const (
	// DefaultSummaryWindow is the reporting window when the caller names none.
	DefaultSummaryWindow = 7 * 24 * time.Hour
	// MaxSummaryWindow bounds one query: this endpoint reads the whole ledger.
	MaxSummaryWindow = 180 * 24 * time.Hour
	// maxSummaryRows caps the buckets returned in one response.
	maxSummaryRows = 500
)

// SummaryFilter is the store-level query.
type SummaryFilter struct {
	UserID  string
	Since   time.Time
	Until   time.Time
	GroupBy string
}

// SummaryRow is one bucket of the roll-up. Every unit keeps its own column for
// the same reason the ledger does: a total that mixes characters with audio
// seconds means nothing.
type SummaryRow struct {
	Key       string `json:"key"`
	Rows      int    `json:"rows"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	AudioSec  int    `json:"audio_sec"`
	Chars     int    `json:"chars"`
	CostFen   int    `json:"cost_fen"`
}

// SummaryResponse is the GET /internal/v1/ai-cost-logs/summary payload.
type SummaryResponse struct {
	GroupBy string       `json:"group_by"`
	Since   string       `json:"since"`
	Until   string       `json:"until"`
	Rows    []SummaryRow `json:"rows"`
	// CostFenIsNotMoney is deliberately loud, because the fen column means two
	// different things depending on the row: voice rows carry 0 (rates pending
	// vendor billing, P2-2), while LLM rows carry an estimate computed from a
	// price table nobody has reconciled against a bill yet. The usage columns
	// are facts; neither fen is an invoice.
	CostFenIsNotMoney string `json:"cost_fen_is_not_money"`
}

const costFenNote = "usage columns are measured facts; cost_fen is neither an invoice nor complete — " +
	"voice rows are 0 pending vendor rates (P2-2), LLM rows are estimates from an unreconciled price table, " +
	"and calls priced by model-name guesswork or left unpriced are counted in orchestrator metrics"

// Summary aggregates the ledger over a window.
func (s *Service) Summary(ctx context.Context, filter SummaryFilter) (SummaryResponse, error) {
	if s.store == nil {
		return SummaryResponse{}, apierr.Internal("aicost store is not configured")
	}
	groupBy := strings.TrimSpace(filter.GroupBy)
	switch groupBy {
	case GroupByTaskType, GroupByModel, GroupByDay:
	case "":
		groupBy = GroupByTaskType
	default:
		return SummaryResponse{}, apierr.InvalidArgument("group_by must be task_type, model or day")
	}
	until := filter.Until
	if until.IsZero() {
		until = s.now().UTC()
	}
	since := filter.Since
	if since.IsZero() {
		since = until.Add(-DefaultSummaryWindow)
	}
	if !since.Before(until) {
		return SummaryResponse{}, apierr.InvalidArgument("since must be before until")
	}
	if until.Sub(since) > MaxSummaryWindow {
		return SummaryResponse{}, apierr.InvalidArgument("window is too wide")
	}
	rows, err := s.store.SummarizeCosts(ctx, SummaryFilter{
		UserID:  strings.TrimSpace(filter.UserID),
		Since:   since.UTC(),
		Until:   until.UTC(),
		GroupBy: groupBy,
	})
	if err != nil {
		return SummaryResponse{}, err
	}
	if len(rows) > maxSummaryRows {
		rows = rows[:maxSummaryRows]
	}
	if rows == nil {
		rows = []SummaryRow{}
	}
	return SummaryResponse{
		GroupBy:           groupBy,
		Since:             since.UTC().Format(time.RFC3339),
		Until:             until.UTC().Format(time.RFC3339),
		Rows:              rows,
		CostFenIsNotMoney: costFenNote,
	}, nil
}

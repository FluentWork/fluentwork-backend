package aicost

import "time"

// Log is one immutable ai_cost_logs ledger row.
type Log struct {
	ID        string    `json:"id"`
	UserID    *string   `json:"user_id,omitempty"`
	TaskType  string    `json:"task_type"`
	Model     string    `json:"model"`
	TokensIn  int       `json:"tokens_in"`
	TokensOut int       `json:"tokens_out"`
	AudioSec  int       `json:"audio_sec"`
	CostFen   int       `json:"cost_fen"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordRequest is the validated input used by Service.Record.
type RecordRequest struct {
	UserID    string
	TaskType  string
	Model     string
	TokensIn  int
	TokensOut int
	AudioSec  int
	CostFen   int
}

package account

import (
	"strings"
	"testing"
)

func TestEnsureParseTimeAddsRequiredParams(t *testing.T) {
	got := ensureParseTime("fw:fw@tcp(127.0.0.1:3306)/fluentwork")
	if !strings.Contains(got, "parseTime=true") || !strings.Contains(got, "loc=UTC") {
		t.Fatalf("got %q", got)
	}
}

func TestEnsureParseTimeKeepsExistingParams(t *testing.T) {
	dsn := "fw:fw@tcp(127.0.0.1:3306)/fluentwork?parseTime=true&loc=Local"
	if got := ensureParseTime(dsn); got != dsn {
		t.Fatalf("got %q", got)
	}
}

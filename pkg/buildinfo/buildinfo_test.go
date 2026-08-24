package buildinfo

import "testing"

func TestGreeting(t *testing.T) {
	if got := Greeting("FluentWork"); got != "Hello, FluentWork." {
		t.Fatalf("Greeting() = %q", got)
	}
}

func TestRepository(t *testing.T) {
	if Repository != "fluentwork-backend" {
		t.Fatalf("Repository = %q", Repository)
	}
}

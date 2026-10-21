package main

import (
	"testing"
)

func TestRunRequiresCommand(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("expected usage error on nil args")
	}
	if err := run([]string{"unknown"}); err == nil {
		t.Fatal("expected usage error on unknown command")
	}
}

func TestRunRequiresDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run([]string{"inspect"}); err == nil {
		t.Fatal("expected database error when DATABASE_URL is unset")
	}
}

func TestRetryRequiresIDs(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://mock:mock@localhost:5432/mock")
	if err := run([]string{"retry"}); err == nil {
		t.Fatal("expected error when retry called without --id and --tenant")
	}
	if err := run([]string{"retry", "--id=not-uuid", "--tenant=not-uuid"}); err == nil {
		t.Fatal("expected error on invalid UUIDs")
	}
}

func TestInvalidLimit(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://mock:mock@localhost:5432/mock")
	if err := run([]string{"inspect", "--limit=0"}); err == nil {
		t.Fatal("expected error on limit < 1")
	}
	if err := run([]string{"inspect", "--limit=2000"}); err == nil {
		t.Fatal("expected error on limit > 1000")
	}
}

package main

import "testing"

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
	if err := run([]string{"balance"}); err == nil {
		t.Fatal("expected database error")
	}
	if err := run([]string{"entries"}); err == nil {
		t.Fatal("expected database error")
	}
	if err := run([]string{"replay"}); err == nil {
		t.Fatal("expected database error")
	}
	if err := run([]string{"verify-replay"}); err == nil {
		t.Fatal("expected database error")
	}
}

func TestRequiredWalletIDs(t *testing.T) {
	if _, _, err := requiredWalletIDs("bad-tenant", "bad-wallet"); err == nil {
		t.Fatal("expected error on invalid tenant and wallet UUIDs")
	}
}

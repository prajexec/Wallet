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

func TestInspectRequiresDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run([]string{"inspect"}); err == nil {
		t.Fatal("expected database error")
	}
}

func TestExceptionsRequiresDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run([]string{"exceptions"}); err == nil {
		t.Fatal("expected database error")
	}
}

func TestParseTenantRejectsInvalidUUID(t *testing.T) {
	if _, err := parseTenant("not-a-uuid"); err == nil {
		t.Fatal("expected UUID validation error")
	}
}

func TestParseIDsRejectsInvalid(t *testing.T) {
	if _, _, err := parseIDs("invalid-tenant", "invalid-transfer"); err == nil {
		t.Fatal("expected error on invalid IDs")
	}
}

func TestReconcileRequiresArguments(t *testing.T) {
	t.Setenv("TRANSFER_INTERNAL_URL", "")
	t.Setenv("INTERNAL_TOKEN", "")
	// Missing url, token, etc.
	if err := run([]string{"reconcile"}); err == nil {
		t.Fatal("expected error on missing reconcile parameters")
	}
}

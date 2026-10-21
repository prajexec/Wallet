package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"digitalwallet/shared/identifiers"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: transferctl inspect|exceptions|reconcile")
	}
	switch args[0] {
	case "inspect":
		return inspect(args[1:])
	case "exceptions":
		return exceptions(args[1:])
	case "reconcile":
		return reconcile(args[1:])
	default:
		return fmt.Errorf("unknown transferctl command %q", args[0])
	}
}
func inspect(args []string) error {
	flags := flag.NewFlagSet("transferctl inspect", flag.ContinueOnError)
	dsn := flags.String("database-url", os.Getenv("DATABASE_URL"), "PostgreSQL DSN")
	tenant := flags.String("tenant", "", "tenant UUIDv7")
	transfer := flags.String("transfer", "", "transfer UUIDv7")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("--database-url or DATABASE_URL is required")
	}
	tenantID, transferID, err := parseIDs(*tenant, *transfer)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var source, dest, currency, ref, state string
	var amount, version int64
	if err := db.QueryRowContext(ctx, `SELECT source_wallet_id,destination_wallet_id,amount_minor,currency,COALESCE(reference,''),state,version FROM transfers WHERE tenant_id=$1 AND transfer_id=$2`, tenantID.String(), transferID.String()).Scan(&source, &dest, &amount, &currency, &ref, &state, &version); err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT step_name,operation_id,state,attempts,COALESCE(last_error,'') FROM transfer_steps WHERE tenant_id=$1 AND transfer_id=$2 ORDER BY step_name`, tenantID.String(), transferID.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	var steps []map[string]any
	for rows.Next() {
		var name, op, stepState, last string
		var attempts int
		if err := rows.Scan(&name, &op, &stepState, &attempts, &last); err != nil {
			return err
		}
		steps = append(steps, map[string]any{"step": name, "operation_id": op, "state": stepState, "attempts": attempts, "last_error": last})
	}
	body, _ := json.MarshalIndent(map[string]any{"tenant_id": tenantID, "transfer_id": transferID, "source_wallet_id": source, "destination_wallet_id": dest, "amount_minor": amount, "currency": currency, "reference": ref, "state": state, "version": version, "steps": steps}, "", "  ")
	fmt.Println(string(body))
	return nil
}
func exceptions(args []string) error {
	flags := flag.NewFlagSet("transferctl exceptions", flag.ContinueOnError)
	dsn := flags.String("database-url", os.Getenv("DATABASE_URL"), "PostgreSQL DSN")
	tenant := flags.String("tenant", "", "tenant UUIDv7")
	exception := flags.String("exception", "", "optional exception UUIDv7 to include notes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dsn) == "" {
		return errors.New("--database-url or DATABASE_URL is required")
	}
	tenantID, err := parseTenant(*tenant)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT exception_id,transfer_id,category,status,is_monetary,owner,created_at,updated_at FROM reconciliation_exceptions WHERE tenant_id=$1 ORDER BY updated_at DESC`, tenantID.String())
	if err != nil {
		return err
	}
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		var id, transfer, category, status string
		var monetary bool
		var owner sql.NullString
		var created, updated time.Time
		if err := rows.Scan(&id, &transfer, &category, &status, &monetary, &owner, &created, &updated); err != nil {
			return err
		}
		result = append(result, map[string]any{"exception_id": id, "transfer_id": transfer, "category": category, "status": status, "is_monetary": monetary, "owner": owner.String, "created_at": created, "updated_at": updated})
	}
	body, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(body))
	if *exception != "" {
		exceptionID, err := parseTenant(*exception)
		if err != nil {
			return err
		}
		notes, err := db.QueryContext(ctx, `SELECT note_id,owner,evidence,created_at FROM reconciliation_exception_notes WHERE tenant_id=$1 AND exception_id=$2 ORDER BY created_at,note_id`, tenantID.String(), exceptionID.String())
		if err != nil {
			return err
		}
		defer notes.Close()
		var noteResult []map[string]any
		for notes.Next() {
			var id, owner string
			var evidence []byte
			var created time.Time
			if err := notes.Scan(&id, &owner, &evidence, &created); err != nil {
				return err
			}
			var decoded any
			if err := json.Unmarshal(evidence, &decoded); err != nil {
				return err
			}
			noteResult = append(noteResult, map[string]any{"note_id": id, "owner": owner, "evidence": decoded, "created_at": created})
		}
		if err := notes.Err(); err != nil {
			return err
		}
		noteBody, _ := json.MarshalIndent(noteResult, "", "  ")
		fmt.Println(string(noteBody))
	}
	return rows.Err()
}
func reconcile(args []string) error {
	flags := flag.NewFlagSet("transferctl reconcile", flag.ContinueOnError)
	base := flags.String("url", os.Getenv("TRANSFER_INTERNAL_URL"), "transfer internal URL")
	token := flags.String("token", os.Getenv("INTERNAL_TOKEN"), "internal token")
	tenant := flags.String("tenant", "", "tenant UUIDv7")
	exception := flags.String("exception", "", "exception UUIDv7")
	operator := flags.String("operator-id", "", "operator ID")
	action := flags.String("action", "", "RESUME_SAGA, COMPENSATE, or NO_ACTION")
	evidence := flags.String("evidence", "{}", "resolution evidence JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tenantID, err := parseTenant(*tenant)
	if err != nil {
		return err
	}
	exceptionID, err := parseTenant(*exception)
	if err != nil {
		return fmt.Errorf("--exception must be a UUIDv7: %w", err)
	}
	if *base == "" || *token == "" || *operator == "" {
		return errors.New("--url, --token, and --operator-id are required")
	}
	if *action != "" && *action != "ACCEPT" && *action != "RESUME_SAGA" && *action != "COMPENSATE" && *action != "NO_ACTION" {
		return errors.New("--action must be ACCEPT, RESUME_SAGA, COMPENSATE, or NO_ACTION")
	}
	var evidenceJSON json.RawMessage
	if !json.Valid([]byte(*evidence)) {
		return errors.New("--evidence must be valid JSON")
	}
	evidenceJSON = json.RawMessage(*evidence)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	headers := map[string]string{"X-Tenant-ID": tenantID.String(), "X-Internal-Token": *token, "X-Operator-ID": *operator}
	body, err := postJSON(ctx, *base+"/internal/v1/reconciliation/exceptions/"+exceptionID.String()+"/investigate", headers, []byte(`{}`))
	if err != nil {
		return err
	}
	if *action == "" {
		fmt.Println(string(body))
		return nil
	}
	if *action == "ACCEPT" {
		payload, marshalErr := json.Marshal(map[string]any{"evidence": evidenceJSON})
		if marshalErr != nil {
			return marshalErr
		}
		body, err = postJSON(ctx, *base+"/internal/v1/reconciliation/exceptions/"+exceptionID.String()+"/accept", headers, payload)
	} else {
		payload, marshalErr := json.Marshal(map[string]any{"action": *action, "evidence": evidenceJSON})
		if marshalErr != nil {
			return marshalErr
		}
		body, err = postJSON(ctx, *base+"/internal/v1/reconciliation/exceptions/"+exceptionID.String()+"/resolve", headers, payload)
	}
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}
func postJSON(ctx context.Context, url string, headers map[string]string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("reconciliation request returned HTTP %d: %s", resp.StatusCode, string(payload))
	}
	return payload, nil
}
func parseTenant(value string) (identifiers.UUID, error) {
	id, err := identifiers.Parse(value)
	if err != nil || id.IsZero() || !id.IsV7() {
		return identifiers.UUID{}, errors.New("value must be a UUIDv7")
	}
	return id, nil
}
func parseIDs(tenant, transfer string) (identifiers.UUID, identifiers.UUID, error) {
	t, err := parseTenant(tenant)
	if err != nil {
		return identifiers.UUID{}, identifiers.UUID{}, fmt.Errorf("--tenant: %w", err)
	}
	id, err := parseTenant(transfer)
	if err != nil {
		return identifiers.UUID{}, identifiers.UUID{}, fmt.Errorf("--transfer: %w", err)
	}
	return t, id, nil
}

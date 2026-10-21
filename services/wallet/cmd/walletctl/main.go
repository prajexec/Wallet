package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
		return errors.New("usage: walletctl balance|entries|replay|verify-replay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	flags := flag.NewFlagSet("walletctl", flag.ContinueOnError)
	dsn := flags.String("database-url", os.Getenv("DATABASE_URL"), "PostgreSQL DSN")
	tenant := flags.String("tenant", "", "tenant UUIDv7")
	wallet := flags.String("wallet", "", "wallet UUIDv7")
	limit := flags.Int("limit", 50, "entry limit")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	database, err := openDatabase(*dsn)
	if err != nil {
		return err
	}
	defer database.Close()
	switch args[0] {
	case "balance":
		tenantID, walletID, err := requiredWalletIDs(*tenant, *wallet)
		if err != nil {
			return err
		}
		return printBalance(ctx, database, tenantID, walletID)
	case "entries":
		tenantID, walletID, err := requiredWalletIDs(*tenant, *wallet)
		if err != nil {
			return err
		}
		return printEntries(ctx, database, tenantID, walletID, *limit)
	case "replay", "verify-replay":
		return replay(ctx, database)
	default:
		return fmt.Errorf("unknown walletctl command %q", args[0])
	}
}

func openDatabase(dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("--database-url or DATABASE_URL is required")
	}
	return sql.Open("pgx", dsn)
}

func requiredWalletIDs(tenantValue, walletValue string) (identifiers.UUID, identifiers.UUID, error) {
	tenant, err := identifiers.Parse(tenantValue)
	if err != nil || tenant.IsZero() || !tenant.IsV7() {
		return identifiers.UUID{}, identifiers.UUID{}, errors.New("--tenant must be a UUIDv7")
	}
	wallet, err := identifiers.Parse(walletValue)
	if err != nil || wallet.IsZero() || !wallet.IsV7() {
		return identifiers.UUID{}, identifiers.UUID{}, errors.New("--wallet must be a UUIDv7")
	}
	return tenant, wallet, nil
}

func printBalance(ctx context.Context, db *sql.DB, tenantID, walletID identifiers.UUID) error {
	var accountID, status, currency string
	if err := db.QueryRowContext(ctx, `SELECT account_id,status FROM wallets WHERE tenant_id=$1 AND id=$2`, tenantID.String(), walletID.String()).Scan(&accountID, &status); err != nil {
		return err
	}
	var posted, held int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN side='CREDIT' THEN amount_minor ELSE -amount_minor END),0) FROM journal_postings WHERE tenant_id=$1 AND account_id=$2 AND currency='USD'`, tenantID.String(), accountID).Scan(&posted); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM wallet_holds WHERE tenant_id=$1 AND wallet_id=$2 AND status='RESERVED'`, tenantID.String(), walletID.String()).Scan(&held); err != nil {
		return err
	}
	currency = "USD"
	return printJSON(map[string]any{"tenant_id": tenantID, "wallet_id": walletID, "status": status, "currency": currency, "current_minor": posted, "held_minor": held, "available_minor": posted - held})
}

func printEntries(ctx context.Context, db *sql.DB, tenantID, walletID identifiers.UUID, limit int) error {
	if limit <= 0 || limit > 1000 {
		return errors.New("--limit must be between 1 and 1000")
	}
	var accountID string
	if err := db.QueryRowContext(ctx, `SELECT account_id FROM wallets WHERE tenant_id=$1 AND id=$2`, tenantID.String(), walletID.String()).Scan(&accountID); err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT jt.id,jt.operation_type,jt.external_reference,jt.created_at,jsonb_agg(jsonb_build_object('id',jp.id,'side',jp.side,'amount_minor',jp.amount_minor,'currency',jp.currency,'entry_type',jp.entry_type,'transfer_id',jp.transfer_id) ORDER BY jp.id) FROM journal_transactions jt JOIN journal_postings jp ON jp.tenant_id=jt.tenant_id AND jp.transaction_id=jt.id WHERE jt.tenant_id=$1 AND jp.account_id=$2 GROUP BY jt.id,jt.operation_type,jt.external_reference,jt.created_at ORDER BY jt.created_at DESC,jt.id DESC LIMIT $3`, tenantID.String(), accountID, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	var entries []map[string]any
	for rows.Next() {
		var id, op string
		var reference sql.NullString
		var created time.Time
		var postings []byte
		if err := rows.Scan(&id, &op, &reference, &created, &postings); err != nil {
			return err
		}
		var decoded any
		if err := json.Unmarshal(postings, &decoded); err != nil {
			return err
		}
		entries = append(entries, map[string]any{"journal_transaction_id": id, "operation_type": op, "reference": reference.String, "created_at": created, "postings": decoded})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return printJSON(entries)
}

func replay(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT tenant_id,transaction_id,currency,COALESCE(SUM(amount_minor) FILTER (WHERE side='DEBIT'),0),COALESCE(SUM(amount_minor) FILTER (WHERE side='CREDIT'),0) FROM journal_postings GROUP BY tenant_id,transaction_id,currency HAVING COALESCE(SUM(amount_minor) FILTER (WHERE side='DEBIT'),0) <> COALESCE(SUM(amount_minor) FILTER (WHERE side='CREDIT'),0)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var unbalanced []map[string]any
	for rows.Next() {
		var tenant, tx, currency string
		var debit, credit int64
		if err := rows.Scan(&tenant, &tx, &currency, &debit, &credit); err != nil {
			return err
		}
		unbalanced = append(unbalanced, map[string]any{"tenant_id": tenant, "transaction_id": tx, "currency": currency, "debits": debit, "credits": credit})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	walletRows, err := db.QueryContext(ctx, `SELECT tenant_id,id,account_id FROM wallets ORDER BY tenant_id,id`)
	if err != nil {
		return err
	}
	defer walletRows.Close()
	var balances []map[string]any
	var balanceMismatch bool
	for walletRows.Next() {
		var tenant, wallet, account string
		if err := walletRows.Scan(&tenant, &wallet, &account); err != nil {
			return err
		}
		var posted, held int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN side='CREDIT' THEN amount_minor ELSE -amount_minor END),0) FROM journal_postings WHERE tenant_id=$1 AND account_id=$2 AND currency='USD'`, tenant, account).Scan(&posted); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM wallet_holds WHERE tenant_id=$1 AND wallet_id=$2 AND status='RESERVED'`, tenant, wallet).Scan(&held); err != nil {
			return err
		}
		available := posted - held
		if available < 0 {
			balanceMismatch = true
		}
		balances = append(balances, map[string]any{"tenant_id": tenant, "wallet_id": wallet, "posted_minor": posted, "held_minor": held, "available_minor": available, "authoritative_match": available >= 0})
	}
	if err := walletRows.Err(); err != nil {
		return err
	}
	result := map[string]any{"unbalanced_transactions": unbalanced, "wallets": balances, "balanced": len(unbalanced) == 0, "adjusted": false}
	if err := printJSON(result); err != nil {
		return err
	}
	if len(unbalanced) > 0 || balanceMismatch {
		return errors.New("journal replay found unbalanced transactions; no balance was adjusted")
	}
	return nil
}

func printJSON(value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}

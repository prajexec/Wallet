package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"digitalwallet/shared/identifiers"
)

var (
	ErrNotFound            = errors.New("transfer not found")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different payload")
)

type Transfer struct {
	TenantID            identifiers.UUID `json:"tenant_id"`
	TransferID          identifiers.UUID `json:"transfer_id"`
	SourceWalletID      identifiers.UUID `json:"source_wallet_id"`
	DestinationWalletID identifiers.UUID `json:"destination_wallet_id"`
	AmountMinor         int64            `json:"amount_minor"`
	Currency            string           `json:"currency"`
	Reference           string           `json:"reference,omitempty"`
	State               string           `json:"state"`
	Version             int64            `json:"version"`
}

type Step struct {
	Name        string
	OperationID identifiers.UUID
	State       string
	Attempts    int
	LastError   string
}

type Repository struct {
	db  *sql.DB
	ids identifiers.Generator
	now func() time.Time
}

func HashRequest(value any) (string, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func New(db *sql.DB, ids identifiers.Generator) *Repository {
	if ids == nil {
		ids = identifiers.NewGenerator()
	}
	return &Repository{db: db, ids: ids, now: func() time.Time { return time.Now().UTC() }}
}

func (r *Repository) Create(ctx context.Context, tenantID identifiers.UUID, key, requestHash string, input Transfer) (Transfer, bool, error) {
	var response Transfer
	replayed := false
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		var storedHash string
		var storedBody []byte
		err := tx.QueryRowContext(ctx, `SELECT request_hash, response_body FROM transfer_operations WHERE tenant_id=$1 AND idempotency_key=$2`, tenantID.String(), key).Scan(&storedHash, &storedBody)
		if err == nil {
			if storedHash != requestHash {
				return ErrIdempotencyConflict
			}
			replayed = true
			return json.Unmarshal(storedBody, &response)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id, err := r.ids.Generate()
		if err != nil {
			return err
		}
		now := r.now()
		input.TenantID, input.TransferID, input.State, input.Version = tenantID, id, "CREATED", 1
		_, err = tx.ExecContext(ctx, `INSERT INTO transfers (tenant_id,transfer_id,source_wallet_id,destination_wallet_id,amount_minor,currency,reference,state,version,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`, tenantID.String(), id.String(), input.SourceWalletID.String(), input.DestinationWalletID.String(), input.AmountMinor, input.Currency, nullableString(input.Reference), input.State, input.Version, now)
		if err != nil {
			return err
		}
		steps := []string{"RESERVE", "PENDING_CREDIT", "CAPTURE", "FINALIZE", "RELEASE", "COMPENSATE"}
		for _, name := range steps {
			op, err := r.ids.Generate()
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO transfer_steps (tenant_id,transfer_id,step_name,state,attempts,operation_id,next_attempt_at) VALUES ($1,$2,$3,'PENDING',0,$4,$5)`, tenantID.String(), id.String(), name, op.String(), now); err != nil {
				return err
			}
		}
		body, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO transfer_operations (tenant_id,idempotency_key,request_hash,transfer_id,response_body,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, tenantID.String(), key, requestHash, id.String(), body, now); err != nil {
			return err
		}
		if err := r.insertEvent(ctx, tx, input, "transfer.created", now); err != nil {
			return err
		}
		response = input
		return nil
	})
	if err != nil {
		return Transfer{}, false, err
	}
	return response, replayed, nil
}

func (r *Repository) Get(ctx context.Context, tenantID, transferID identifiers.UUID) (Transfer, error) {
	var t Transfer
	var source, dest, id string
	var ref sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT transfer_id,source_wallet_id,destination_wallet_id,amount_minor,currency,reference,state,version FROM transfers WHERE tenant_id=$1 AND transfer_id=$2`, tenantID.String(), transferID.String()).Scan(&id, &source, &dest, &t.AmountMinor, &t.Currency, &ref, &t.State, &t.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	if err != nil {
		return Transfer{}, err
	}
	t.TenantID = tenantID
	t.TransferID = identifiers.MustParse(id)
	t.SourceWalletID = identifiers.MustParse(source)
	t.DestinationWalletID = identifiers.MustParse(dest)
	t.Reference = ref.String
	return t, nil
}

func (r *Repository) Steps(ctx context.Context, tenantID, transferID identifiers.UUID) ([]Step, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT step_name,operation_id,state,attempts,COALESCE(last_error,'') FROM transfer_steps WHERE tenant_id=$1 AND transfer_id=$2 ORDER BY CASE step_name WHEN 'RESERVE' THEN 1 WHEN 'PENDING_CREDIT' THEN 2 WHEN 'CAPTURE' THEN 3 WHEN 'FINALIZE' THEN 4 ELSE 5 END`, tenantID.String(), transferID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Step
	for rows.Next() {
		var s Step
		var op string
		if err := rows.Scan(&s.Name, &op, &s.State, &s.Attempts, &s.LastError); err != nil {
			return nil, err
		}
		s.OperationID = identifiers.MustParse(op)
		result = append(result, s)
	}
	return result, rows.Err()
}

func (r *Repository) SetState(ctx context.Context, tenantID, transferID identifiers.UUID, state string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE transfers SET state=$3,version=version+1,updated_at=$4 WHERE tenant_id=$1 AND transfer_id=$2`, tenantID.String(), transferID.String(), state, r.now())
	return err
}
func (r *Repository) BumpStep(ctx context.Context, tenantID, transferID identifiers.UUID, name, state, lastError string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE transfer_steps SET state=$4,attempts=attempts+1,last_error=$5,next_attempt_at=$6 WHERE tenant_id=$1 AND transfer_id=$2 AND step_name=$3`, tenantID.String(), transferID.String(), name, state, nullableString(lastError), r.now())
	return err
}

func (r *Repository) Reconciliation(ctx context.Context, tenantID, transferID identifiers.UUID, category string, evidence any) error {
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	id, err := r.ids.Generate()
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO reconciliation_exceptions (tenant_id,exception_id,transfer_id,category,status,evidence,created_at) VALUES ($1,$2,$3,$4,'OPEN',$5,$6)`, tenantID.String(), id.String(), transferID.String(), category, body, r.now())
	return err
}

func (r *Repository) insertEvent(ctx context.Context, tx *sql.Tx, t Transfer, eventType string, now time.Time) error {
	body, err := json.Marshal(t)
	if err != nil {
		return err
	}
	id, err := r.ids.Generate()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO transfer_events (tenant_id,event_id,transfer_id,event_type,schema_version,aggregate_version,payload,occurred_at) VALUES ($1,$2,$3,$4,1,$5,$6,$7)`, t.TenantID.String(), id.String(), t.TransferID.String(), eventType, t.Version, body, now)
	return err
}
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

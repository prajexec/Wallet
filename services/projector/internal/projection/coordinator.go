package projection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"digitalwallet/services/projector/internal/storage/generated"
	"digitalwallet/shared/events"
	"digitalwallet/shared/identifiers"
)

type Coordinator struct {
	DB                  *sql.DB
	ReplayConsumerName  string
	WalletVerifyURLs    []string
	WalletVerifyToken   string
	HTTPClient          *http.Client
	VerificationTimeout time.Duration
}

type RebuildResult struct {
	RebuildID  identifiers.UUID `json:"rebuild_id"`
	TargetSlot string           `json:"target_slot"`
	State      string           `json:"state"`
}

type authoritativeWallet struct {
	TenantID           identifiers.UUID `json:"tenant_id"`
	WalletID           identifiers.UUID `json:"wallet_id"`
	Currency           string           `json:"currency"`
	PostedBalanceMinor int64            `json:"posted_balance_minor"`
	AsOfVersion        int64            `json:"as_of_version"`
}

type verificationPage struct {
	Wallets           []authoritativeWallet `json:"wallets"`
	NextAfterTenantID *identifiers.UUID     `json:"next_after_tenant_id"`
	NextAfterWalletID *identifiers.UUID     `json:"next_after_wallet_id"`
}

func (c *Coordinator) Rebuild(ctx context.Context, autoPromote bool) (RebuildResult, error) {
	if c == nil || c.DB == nil {
		return RebuildResult{}, errors.New("projection coordinator is not configured")
	}
	q := storage.New(c.DB)
	version, err := q.GetProjectionVersion(ctx, storage.GetProjectionVersionParams{ProjectionName: "wallet"})
	if err != nil {
		return RebuildResult{}, err
	}
	target := version.PreviousSlot
	rebuildID, err := identifiers.NewUUIDv7()
	if err != nil {
		return RebuildResult{}, err
	}
	now := time.Now().UTC()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return RebuildResult{}, err
	}
	txq := storage.New(tx)
	if err := txq.DeleteWalletEntrySlot(ctx, storage.DeleteWalletEntrySlotParams{ProjectionSlot: target}); err != nil {
		_ = tx.Rollback()
		return RebuildResult{}, err
	}
	if err := txq.DeleteWalletBalanceSlot(ctx, storage.DeleteWalletBalanceSlotParams{ProjectionSlot: target}); err != nil {
		_ = tx.Rollback()
		return RebuildResult{}, err
	}
	if err := txq.InsertProjectionRebuild(ctx, storage.InsertProjectionRebuildParams{RebuildID: rebuildID, ProjectionName: "wallet", TargetSlot: target, State: "RUNNING", ValidationReport: json.RawMessage(`{}`), StartedAt: now}); err != nil {
		_ = tx.Rollback()
		return RebuildResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RebuildResult{}, err
	}

	if err := c.replay(ctx, version.ActiveSlot, target); err != nil {
		_ = c.setRebuildState(ctx, rebuildID, "FAILED", map[string]any{"error": err.Error()}, true)
		return RebuildResult{RebuildID: rebuildID, TargetSlot: target, State: "FAILED"}, err
	}
	if err := c.setRebuildState(ctx, rebuildID, "VERIFYING", map[string]any{}, false); err != nil {
		return RebuildResult{}, err
	}
	if err := c.Verify(ctx, target); err != nil {
		_ = c.setRebuildState(ctx, rebuildID, "FAILED", map[string]any{"error": err.Error()}, true)
		return RebuildResult{RebuildID: rebuildID, TargetSlot: target, State: "FAILED"}, err
	}
	if err := c.setRebuildState(ctx, rebuildID, "READY", map[string]any{"verified_at": time.Now().UTC()}, false); err != nil {
		return RebuildResult{}, err
	}
	result := RebuildResult{RebuildID: rebuildID, TargetSlot: target, State: "READY"}
	if autoPromote {
		if err := c.Promote(ctx, rebuildID); err != nil {
			return result, err
		}
		result.State = "PROMOTED"
	}
	return result, nil
}

func (c *Coordinator) replay(ctx context.Context, sourceSlot, targetSlot string) error {
	consumerName := c.ReplayConsumerName
	if consumerName == "" {
		consumerName = "projector-balance-" + sourceSlot
	}
	rows, err := c.DB.QueryContext(ctx, `SELECT payload FROM inbox_messages WHERE consumer_name = $1 ORDER BY tenant_id, aggregate_id, aggregate_version`, consumerName)
	if err != nil {
		return fmt.Errorf("load replay events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		var envelope structEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return fmt.Errorf("decode replay event: %w", err)
		}
		tx, err := c.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		handler, err := NewHandler(targetSlot)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := handler.HandleEvent(ctx, tx, envelope.Envelope); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return rows.Err()
}

type structEnvelope struct{ events.Envelope }

func (c *Coordinator) Verify(ctx context.Context, slot string) error {
	deadline := time.Now().Add(c.verificationTimeout())
	for {
		authoritative, err := c.authoritativeWallets(ctx)
		if err != nil {
			return err
		}
		behind, err := c.compareSlot(ctx, slot, authoritative)
		if err != nil {
			return err
		}
		if !behind || time.Now().After(deadline) {
			if behind {
				return errors.New("projection is behind authoritative wallet versions")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *Coordinator) verificationTimeout() time.Duration {
	if c.VerificationTimeout > 0 {
		return c.VerificationTimeout
	}
	return 30 * time.Second
}

func (c *Coordinator) authoritativeWallets(ctx context.Context) (map[string]authoritativeWallet, error) {
	result := make(map[string]authoritativeWallet)
	if len(c.WalletVerifyURLs) == 0 {
		return result, nil
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	for _, base := range c.WalletVerifyURLs {
		var afterTenant, afterWallet string
		for {
			u, err := url.Parse(strings.TrimRight(base, "/") + "/internal/v1/wallets/verify")
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("limit", "500")
			if afterTenant != "" {
				q.Set("after_tenant_id", afterTenant)
				q.Set("after_wallet_id", afterWallet)
			}
			u.RawQuery = q.Encode()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("X-Internal-Token", c.WalletVerifyToken)
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			var page verificationPage
			decodeErr := json.NewDecoder(resp.Body).Decode(&page)
			resp.Body.Close()
			if decodeErr != nil {
				return nil, decodeErr
			}
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("wallet verification endpoint returned %s", resp.Status)
			}
			for _, row := range page.Wallets {
				result[row.TenantID.String()+"/"+row.WalletID.String()] = row
			}
			if page.NextAfterTenantID == nil || page.NextAfterWalletID == nil {
				break
			}
			afterTenant, afterWallet = page.NextAfterTenantID.String(), page.NextAfterWalletID.String()
		}
	}
	return result, nil
}

func (c *Coordinator) compareSlot(ctx context.Context, slot string, authoritative map[string]authoritativeWallet) (bool, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT tenant_id, wallet_id, currency, posted_balance_minor, last_applied_version FROM wallet_balance_view WHERE projection_slot = $1`, slot)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	behind := false
	for rows.Next() {
		var tenantID, walletID, currency string
		var posted, version int64
		if err := rows.Scan(&tenantID, &walletID, &currency, &posted, &version); err != nil {
			return false, err
		}
		key := tenantID + "/" + walletID
		seen[key] = true
		if expected, ok := authoritative[key]; ok {
			if version < expected.AsOfVersion {
				behind = true
			} else if version > expected.AsOfVersion || posted != expected.PostedBalanceMinor || currency != expected.Currency {
				return false, fmt.Errorf("projection mismatch for %s", key)
			}
		}
		var entrySum, entryCount, maxEntry int64
		if err := c.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN side = 'CREDIT' THEN amount_minor ELSE -amount_minor END),0), COUNT(*), COALESCE(MAX(entry_version),0) FROM wallet_entry_view WHERE projection_slot=$1 AND tenant_id=$2::uuid AND wallet_id=$3::uuid`, slot, tenantID, walletID).Scan(&entrySum, &entryCount, &maxEntry); err != nil {
			return false, err
		}
		if entrySum != posted || entryCount != maxEntry {
			return false, fmt.Errorf("entry invariant mismatch for %s", key)
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for key := range authoritative {
		if !seen[key] {
			return false, fmt.Errorf("authoritative wallet missing from projection: %s", key)
		}
	}
	return behind, nil
}

func (c *Coordinator) setRebuildState(ctx context.Context, id identifiers.UUID, state string, report map[string]any, complete bool) error {
	data, _ := json.Marshal(report)
	now := time.Now().UTC()
	var completed *time.Time
	if complete {
		completed = &now
	}
	return storage.New(c.DB).UpdateProjectionRebuild(ctx, storage.UpdateProjectionRebuildParams{RebuildID: id, State: state, ValidationReport: data, UpdatedAt: now, CompletedAt: completed})
}

func (c *Coordinator) Promote(ctx context.Context, rebuildID identifiers.UUID) error {
	rebuild, err := storage.New(c.DB).GetProjectionRebuild(ctx, storage.GetProjectionRebuildParams{RebuildID: rebuildID})
	if err != nil {
		return err
	}
	if rebuild.State != "READY" {
		return fmt.Errorf("rebuild %s is %s, want READY", rebuildID, rebuild.State)
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := storage.New(tx)
	version, err := q.GetProjectionVersion(ctx, storage.GetProjectionVersionParams{ProjectionName: "wallet"})
	if err != nil {
		return err
	}
	if err := q.UpdateProjectionVersion(ctx, storage.UpdateProjectionVersionParams{ProjectionName: "wallet", ActiveSlot: rebuild.TargetSlot, PreviousSlot: version.ActiveSlot, PromotedAt: time.Now().UTC()}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.setRebuildState(ctx, rebuildID, "PROMOTED", map[string]any{"promoted_at": time.Now().UTC()}, true)
}

func (c *Coordinator) Rollback(ctx context.Context) error {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := storage.New(tx)
	version, err := q.GetProjectionVersion(ctx, storage.GetProjectionVersionParams{ProjectionName: "wallet"})
	if err != nil {
		return err
	}
	if err := q.UpdateProjectionVersion(ctx, storage.UpdateProjectionVersionParams{ProjectionName: "wallet", ActiveSlot: version.PreviousSlot, PreviousSlot: version.ActiveSlot, PromotedAt: time.Now().UTC()}); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Coordinator) WipeActive(ctx context.Context) error {
	version, err := storage.New(c.DB).GetProjectionVersion(ctx, storage.GetProjectionVersionParams{ProjectionName: "wallet"})
	if err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := storage.New(tx)
	if err := q.DeleteWalletEntrySlot(ctx, storage.DeleteWalletEntrySlotParams{ProjectionSlot: version.ActiveSlot}); err != nil {
		return err
	}
	if err := q.DeleteWalletBalanceSlot(ctx, storage.DeleteWalletBalanceSlotParams{ProjectionSlot: version.ActiveSlot}); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Coordinator) RetireInactive(ctx context.Context) error {
	version, err := storage.New(c.DB).GetProjectionVersion(ctx, storage.GetProjectionVersionParams{ProjectionName: "wallet"})
	if err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := storage.New(tx)
	if err := q.DeleteWalletEntrySlot(ctx, storage.DeleteWalletEntrySlotParams{ProjectionSlot: version.PreviousSlot}); err != nil {
		return err
	}
	if err := q.DeleteWalletBalanceSlot(ctx, storage.DeleteWalletBalanceSlotParams{ProjectionSlot: version.PreviousSlot}); err != nil {
		return err
	}
	return tx.Commit()
}

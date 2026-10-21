package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type client struct {
	http             *http.Client
	wallet           string
	walletStats      string
	destination      string
	transfer         string
	transferInternal string
	projector        string
	internal         string
	operator         string
}

type apiError struct{ status int }

func (e *apiError) Error() string { return fmt.Sprintf("http status %d", e.status) }

type wallet struct {
	WalletID         string `json:"wallet_id"`
	AggregateVersion int64  `json:"aggregate_version"`
}

type transfer struct {
	TransferID string `json:"transfer_id"`
}

func uuidv7() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli()))
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

func (c *client) request(method, url, tenant, idempotency string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-ID", tenant)
	}
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	if c.operator != "" {
		req.Header.Set("X-Operator-ID", c.operator)
	}
	if c.internal != "" {
		req.Header.Set("X-Internal-Token", c.internal)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return &apiError{status: response.StatusCode}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (c *client) createWallet(base, tenant, key string) (wallet, error) {
	var result wallet
	err := c.request(http.MethodPost, base+"/v1/wallets", tenant, key, map[string]string{"external_reference": key}, &result)
	return result, err
}

func (c *client) deposit(base, tenant, walletID, key string, amount int64) (int64, error) {
	var result struct {
		AggregateVersion int64 `json:"aggregate_version"`
	}
	err := c.request(http.MethodPost, base+"/v1/wallets/"+walletID+"/deposit", tenant, key, map[string]any{"amount_minor": amount, "currency": "USD"}, &result)
	return result.AggregateVersion, err
}

func (c *client) withdraw(base, tenant, walletID, key string, amount int64) error {
	return c.request(http.MethodPost, base+"/v1/wallets/"+walletID+"/withdraw", tenant, key, map[string]any{"amount_minor": amount, "currency": "USD"}, nil)
}

func (c *client) balance(base, tenant, walletID string, out any) error {
	return c.request(http.MethodGet, base+"/v1/wallets/"+walletID+"/balance", tenant, "", nil, out)
}

func (c *client) createTransfer(tenant, source, destination, key string, amount int64) (transfer, error) {
	var result transfer
	err := c.request(http.MethodPost, c.transfer+"/v1/transfers", tenant, key, map[string]any{
		"source_wallet_id": source, "destination_wallet_id": destination, "amount_minor": amount, "currency": "USD", "reference": key,
	}, &result)
	return result, err
}

func (c *client) transferState(tenant, id string) (string, error) {
	var result struct {
		State string `json:"state"`
	}
	err := c.request(http.MethodGet, c.transfer+"/v1/transfers/"+id, tenant, "", nil, &result)
	return result.State, err
}

func (c *client) projectedBalance(tenant, walletID string, version int64) error {
	url := fmt.Sprintf("%s/v1/read/wallets/%s/balance?min_version=%d&wait_ms=500", c.projector, walletID, version)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err := c.request(http.MethodGet, url, tenant, "", nil, nil)
		if err == nil {
			return nil
		}
		var apiErr *apiError
		if !errors.As(err, &apiErr) || (apiErr.status != http.StatusConflict && apiErr.status != http.StatusNotFound) {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("projection did not catch up before timeout")
}

func (c *client) stats(base string) (map[string]any, error) {
	var result map[string]any
	err := c.request(http.MethodGet, base+"/internal/v1/stats", "", "", nil, &result)
	return result, err
}

func (c *client) walletInternal() string { return c.walletStats }

func (c *client) exceptions(tenant string) ([]map[string]any, error) {
	var result []map[string]any
	err := c.request(http.MethodGet, c.transferInternal+"/internal/v1/reconciliation/exceptions", tenant, "", nil, &result)
	return result, err
}

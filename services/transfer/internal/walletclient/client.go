package walletclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"digitalwallet/shared/identifiers"
)

type Class int

const (
	Success Class = iota
	Transient
	Permanent
	Ambiguous
)

type Error struct {
	Class  Class
	Status int
	Err    error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

type Client struct {
	baseURL, token string
	http           *http.Client
}
type Command struct {
	TransferID        identifiers.UUID `json:"transfer_id"`
	AmountMinor       int64            `json:"amount_minor"`
	Currency          string           `json:"currency"`
	Reference         string           `json:"reference,omitempty"`
	TargetOperationID identifiers.UUID `json:"target_operation_id,omitempty"`
}
type Response struct {
	OperationID          identifiers.UUID  `json:"operation_id"`
	Status               string            `json:"status"`
	JournalTransactionID *identifiers.UUID `json:"journal_transaction_id,omitempty"`
}

func New(baseURL, token string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: client}
}
func (c *Client) Do(ctx context.Context, tenantID, walletID, operationID identifiers.UUID, action string, command Command) (Response, error) {
	body, err := json.Marshal(command)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/v1/wallets/"+walletID.String()+"/"+action, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID.String())
	req.Header.Set("X-Internal-Token", c.token)
	req.Header.Set("X-Operation-ID", operationID.String())
	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, &Error{Class: Ambiguous, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var out Response
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return Response{}, err
		}
		return out, nil
	}
	class := Permanent
	if resp.StatusCode >= 500 {
		class = Transient
	}
	return Response{}, &Error{Class: class, Status: resp.StatusCode, Err: fmt.Errorf("wallet %s returned HTTP %d", action, resp.StatusCode)}
}
func IsClass(err error, class Class) bool {
	var target *Error
	return errors.As(err, &target) && target.Class == class
}
func TimeoutClient(timeout time.Duration) *http.Client { return &http.Client{Timeout: timeout} }

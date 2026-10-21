package walletclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"digitalwallet/shared/identifiers"
)

func TestClientDo(t *testing.T) {
	ids := identifiers.NewGenerator()
	tenantID, _ := ids.Generate()
	walletID, _ := ids.Generate()
	opID, _ := ids.Generate()
	transferID, _ := ids.Generate()

	cases := []struct {
		name       string
		statusCode int
		body       any
		hang       bool
		wantClass  Class
		wantErr    bool
	}{
		{
			name:       "success 201",
			statusCode: http.StatusCreated,
			body: Response{
				OperationID: opID,
				Status:      "SUCCESS",
			},
			wantClass: Success,
			wantErr:   false,
		},
		{
			name:       "permanent error 409",
			statusCode: http.StatusConflict,
			body:       map[string]string{"error": "conflict"},
			wantClass:  Permanent,
			wantErr:    true,
		},
		{
			name:       "transient error 503",
			statusCode: http.StatusServiceUnavailable,
			body:       map[string]string{"error": "service unavailable"},
			wantClass:  Transient,
			wantErr:    true,
		},
		{
			name:      "ambiguous error timeout",
			hang:      true,
			wantClass: Ambiguous,
			wantErr:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.hang {
					time.Sleep(100 * time.Millisecond)
					return
				}
				if r.Header.Get("X-Internal-Token") != "my-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.Header.Get("X-Tenant-ID") != tenantID.String() {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Header.Get("X-Operation-ID") != opID.String() {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				_ = json.NewEncoder(w).Encode(tc.body)
			}))
			defer server.Close()

			httpClient := server.Client()
			if tc.hang {
				httpClient = &http.Client{Timeout: 20 * time.Millisecond}
			}
			client := New(server.URL, "my-token", httpClient)

			cmd := Command{
				TransferID:  transferID,
				AmountMinor: 1000,
				Currency:    "USD",
			}

			resp, err := client.Do(context.Background(), tenantID, walletID, opID, "reservations", cmd)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !IsClass(err, tc.wantClass) {
					t.Fatalf("error class mismatch: err = %v, wantClass = %v", err, tc.wantClass)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.OperationID != opID || resp.Status != "SUCCESS" {
					t.Fatalf("unexpected response: %+v", resp)
				}
			}
		})
	}
}

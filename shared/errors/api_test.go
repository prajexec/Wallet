package errors

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestErrorResponseJSONRoundTrip(t *testing.T) {
	want := New("insufficient_funds", "wallet does not have enough available USD", "req_01", false, nil)
	encoded, err := Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"error"`) || !strings.Contains(string(encoded), `"details":{}`) {
		t.Fatalf("Marshal() = %s", encoded)
	}
	got, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !reflect.DeepEqual(got.Error, want) {
		t.Fatalf("Parse() = %+v, want %+v", got.Error, want)
	}

	// The outer type is also safe to use directly with encoding/json.
	encodedAgain, err := json.Marshal(ErrorResponse{Error: want})
	if err != nil || string(encodedAgain) != string(encoded) {
		t.Fatalf("json.Marshal(ErrorResponse) = %s, %v; want %s", encodedAgain, err, encoded)
	}
}

func TestAPIErrorValidation(t *testing.T) {
	tests := []struct {
		name string
		err  APIError
	}{
		{name: "missing code", err: New("", "message", "req", false, nil)},
		{name: "missing message", err: New("code", "", "req", false, nil)},
		{name: "missing request ID", err: New("code", "message", "", false, nil)},
		{name: "nil details literal", err: APIError{Code: "code", Message: "message", RequestID: "req"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.err.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	apiErr := New("validation_error", "amount must be positive", "req_02", false, map[string]any{"field": "amount_minor"})
	if err := WriteJSON(recorder, 422, apiErr); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	if recorder.Code != 422 {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if _, err := Parse(recorder.Body.Bytes()); err != nil {
		t.Fatalf("Parse(response body) error = %v", err)
	}
}

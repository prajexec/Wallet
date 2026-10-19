// Package errors contains the shared JSON error response used by the HTTP
// contracts. The package intentionally depends only on net/http and the
// standard JSON library so service handlers can use it directly.
package errors

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"strings"
)

var (
	// ErrInvalidAPIError identifies an error response that cannot satisfy the
	// documented API shape.
	ErrInvalidAPIError = stderrors.New("invalid API error")
	// ErrMissingAPIErrorField identifies a required error field that is absent.
	ErrMissingAPIErrorField = stderrors.New("missing API error field")
)

const (
	// Common error codes from the API contract and its documented semantics.
	CodeValidation           = "validation_error"
	CodeInsufficientFunds    = "insufficient_funds"
	CodeRequestInProgress    = "request_in_progress"
	CodeIdempotencyKeyReused = "idempotency_key_reused"
)

// APIError is the inner error object in the HTTP response contract.
type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// ErrorResponse is the complete documented HTTP error body.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// Response is a short alias for ErrorResponse.
type Response = ErrorResponse

// New constructs a shared API error. A nil details map is encoded as the
// contract's empty object rather than JSON null.
func New(code, message, requestID string, retryable bool, details map[string]any) APIError {
	if details == nil {
		details = map[string]any{}
	} else {
		details = cloneDetails(details)
	}
	return APIError{
		Code:      code,
		Message:   message,
		RequestID: requestID,
		Retryable: retryable,
		Details:   details,
	}
}

// WithDetails returns a copy with a cloned details map.
func (e APIError) WithDetails(details map[string]any) APIError {
	e.Details = cloneDetails(details)
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	return e
}

// Error implements the standard error interface.
func (e APIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// Validate checks the required fields and ensures details is an object.
func (e APIError) Validate() error {
	if strings.TrimSpace(e.Code) == "" {
		return fmt.Errorf("%w: code", ErrMissingAPIErrorField)
	}
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Errorf("%w: message", ErrMissingAPIErrorField)
	}
	if strings.TrimSpace(e.RequestID) == "" {
		return fmt.Errorf("%w: request_id", ErrMissingAPIErrorField)
	}
	if e.Details == nil {
		return fmt.Errorf("%w: details", ErrInvalidAPIError)
	}
	return nil
}

// Validate checks the wrapped API error.
func (r ErrorResponse) Validate() error {
	if err := r.Error.Validate(); err != nil {
		return err
	}
	return nil
}

// MarshalJSON validates and emits the inner error object. Use Marshal or an
// ErrorResponse when the complete {"error": ...} body is required.
func (e APIError) MarshalJSON() ([]byte, error) {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wire APIError
	return json.Marshal(wire(e))
}

// UnmarshalJSON decodes and validates the inner error object.
func (e *APIError) UnmarshalJSON(value []byte) error {
	type wire APIError
	var decoded wire
	if err := json.Unmarshal(value, &decoded); err != nil {
		return err
	}
	result := APIError(decoded)
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	if err := result.Validate(); err != nil {
		return err
	}
	if e == nil {
		return stderrors.New("nil API error destination")
	}
	*e = result
	return nil
}

// Marshal encodes the complete shared API error body.
func Marshal(err APIError) ([]byte, error) {
	return json.Marshal(ErrorResponse{Error: err})
}

// Parse decodes the complete shared API error body.
func Parse(value []byte) (ErrorResponse, error) {
	var response ErrorResponse
	if err := json.Unmarshal(value, &response); err != nil {
		return ErrorResponse{}, err
	}
	return response, nil
}

// MarshalJSON validates and emits the outer error envelope.
func (r ErrorResponse) MarshalJSON() ([]byte, error) {
	if r.Error.Details == nil {
		r.Error.Details = map[string]any{}
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire ErrorResponse
	return json.Marshal(wire(r))
}

// UnmarshalJSON decodes and validates the outer error envelope.
func (r *ErrorResponse) UnmarshalJSON(value []byte) error {
	type wire ErrorResponse
	var decoded wire
	if err := json.Unmarshal(value, &decoded); err != nil {
		return err
	}
	result := ErrorResponse(decoded)
	if err := result.Validate(); err != nil {
		return err
	}
	if r == nil {
		return stderrors.New("nil API error response destination")
	}
	*r = result
	return nil
}

// WriteJSON writes a complete API error response with the supplied HTTP
// status. It sets Content-Type before writing the status and body.
func WriteJSON(w http.ResponseWriter, status int, apiErr APIError) error {
	if w == nil {
		return stderrors.New("nil HTTP response writer")
	}
	body, err := Marshal(apiErr)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	clone := make(map[string]any, len(details))
	for key, value := range details {
		clone[key] = value
	}
	return clone
}

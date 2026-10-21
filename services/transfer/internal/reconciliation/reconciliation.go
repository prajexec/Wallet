package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/services/transfer/internal/saga"
	"digitalwallet/services/transfer/internal/walletclient"
	"digitalwallet/shared/identifiers"
)

type Outcome string

const (
	OutcomeUnknown   Outcome = "UNKNOWN"
	OutcomeAbsent    Outcome = "ABSENT"
	OutcomeConfirmed Outcome = "CONFIRMED"
)

type Decision struct {
	Outcome    Outcome
	NextState  string
	Resume     bool
	Compensate bool
}

func Decide(step string, operation walletclient.Response, err error) Decision {
	if err != nil {
		var walletErr *walletclient.Error
		if errors.As(err, &walletErr) && walletErr.Status == 404 {
			return Decision{Outcome: OutcomeAbsent, Resume: true}
		}
		return Decision{Outcome: OutcomeUnknown}
	}
	if operation.Status != "COMPLETED" {
		return Decision{Outcome: OutcomeUnknown}
	}
	state := map[string]string{"RESERVE": "RESERVED", "PENDING_CREDIT": "PENDING_CREDITED", "CAPTURE": "CAPTURED", "FINALIZE": "COMPLETED", "RELEASE": "COMPENSATED", "COMPENSATE": "COMPENSATED"}[step]
	if state == "" {
		return Decision{Outcome: OutcomeUnknown}
	}
	return Decision{Outcome: OutcomeConfirmed, NextState: state, Resume: step != "FINALIZE" && step != "COMPENSATE"}
}

type Service struct {
	repo        *repository.Repository
	saga        *saga.Service
	source      *walletclient.Client
	destination *walletclient.Client
}

func (s *Service) Sweep(ctx context.Context, owner string) error {
	exceptions, err := s.repo.ListAllActiveExceptions(ctx, 100)
	if err != nil {
		return err
	}
	for _, exception := range exceptions {
		if exception.Status != repository.ExceptionOpen {
			continue
		}
		if err := s.Reconcile(ctx, exception.TenantID, exception.ExceptionID, owner); err != nil && !errors.Is(err, repository.ErrConflict) {
			return err
		}
	}
	return nil
}

func New(repo *repository.Repository, orchestrator *saga.Service, source, destination *walletclient.Client) *Service {
	return &Service{repo: repo, saga: orchestrator, source: source, destination: destination}
}

func (s *Service) Reconcile(ctx context.Context, tenantID, exceptionID identifiers.UUID, owner string) error {
	exception, err := s.repo.GetException(ctx, tenantID, exceptionID)
	if err != nil {
		return err
	}
	if exception.Status == repository.ExceptionOpen {
		exception, err = s.repo.ClaimException(ctx, tenantID, exceptionID, owner)
		if err != nil {
			return err
		}
	} else if exception.Owner != owner {
		return repository.ErrConflict
	}
	transfer, err := s.repo.Get(ctx, tenantID, exception.TransferID)
	if err != nil {
		return err
	}
	steps, err := s.repo.Steps(ctx, tenantID, transfer.TransferID)
	if err != nil {
		return err
	}
	stepName := exception.Category
	if exception.Category == "COMPENSATION_FAILED" {
		var evidence struct {
			Stage string `json:"stage"`
		}
		_ = json.Unmarshal(exception.Evidence, &evidence)
		if evidence.Stage == "release_reserve" {
			stepName = "RELEASE"
		} else {
			stepName = "COMPENSATE"
		}
	}
	for _, step := range steps {
		if step.Name == stepName {
			stepName = step.Name
			return s.reconcileStep(ctx, tenantID, transfer, exception, step, stepName, owner)
		}
	}
	return fmt.Errorf("no transfer step for reconciliation category %q", stepName)
}

func (s *Service) reconcileStep(ctx context.Context, tenantID identifiers.UUID, transfer repository.Transfer, exception repository.ReconciliationException, step repository.Step, stepName, owner string) error {
	client := s.source
	expectedWalletID := transfer.SourceWalletID
	if stepName == "PENDING_CREDIT" || stepName == "FINALIZE" {
		client = s.destination
		expectedWalletID = transfer.DestinationWalletID
	}
	var operation walletclient.Response
	var lookupErr error
	if client == nil {
		lookupErr = fmt.Errorf("wallet client is not configured")
	} else {
		operation, lookupErr = client.GetOperation(ctx, tenantID, step.OperationID)
	}
	if lookupErr == nil && !operationMatches(operation, tenantID, transfer, expectedWalletID, stepName) {
		lookupErr = fmt.Errorf("wallet operation evidence does not match transfer step")
	}
	decision := Decide(stepName, operation, lookupErr)
	if decision.Outcome == OutcomeUnknown {
		return s.repo.AddExceptionNote(ctx, exception, owner, map[string]any{"operation_id": step.OperationID.String(), "outcome": decision.Outcome, "error": errorString(lookupErr)})
	}
	if decision.Outcome == OutcomeAbsent {
		if err := s.repo.SetState(ctx, tenantID, transfer.TransferID, stateBefore(stepName)); err != nil {
			return err
		}
		if err := s.repo.AddExceptionNote(ctx, exception, owner, map[string]any{"operation_id": step.OperationID.String(), "outcome": decision.Outcome}); err != nil {
			return err
		}
		if s.saga == nil {
			return nil
		}
		return s.saga.Run(ctx, tenantID, transfer.TransferID)
	}
	if err := s.repo.BumpStep(ctx, tenantID, transfer.TransferID, stepName, "COMPLETED", "reconciled operation confirmed"); err != nil {
		return err
	}
	if err := s.repo.SetState(ctx, tenantID, transfer.TransferID, decision.NextState); err != nil {
		return err
	}
	if err := s.repo.AddExceptionNote(ctx, exception, owner, map[string]any{"operation_id": step.OperationID.String(), "outcome": decision.Outcome, "journal_transaction_id": operation.JournalTransactionID}); err != nil {
		return err
	}
	if decision.Resume && s.saga != nil {
		return s.saga.Run(ctx, tenantID, transfer.TransferID)
	}
	return nil
}

func stateBefore(step string) string {
	return map[string]string{"RESERVE": "CREATED", "PENDING_CREDIT": "RESERVED", "CAPTURE": "PENDING_CREDITED", "FINALIZE": "CAPTURED"}[step]
}
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func operationMatches(operation walletclient.Response, tenantID identifiers.UUID, transfer repository.Transfer, walletID identifiers.UUID, step string) bool {
	kind := map[string]string{"RESERVE": "RESERVE", "PENDING_CREDIT": "PENDING_CREDIT", "CAPTURE": "CAPTURE", "FINALIZE": "FINALIZE", "RELEASE": "RELEASE", "COMPENSATE": "COMPENSATE"}[step]
	return operation.TenantID == tenantID && operation.WalletID == walletID && operation.TransferID == transfer.TransferID && operation.AmountMinor == transfer.AmountMinor && operation.Currency == transfer.Currency && operation.OperationKind == kind && operation.Status == "COMPLETED" && (kind == "RESERVE" || kind == "PENDING_CREDIT" || operation.JournalTransactionID != nil)
}

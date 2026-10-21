package saga

import (
	"context"
	"fmt"
	"time"

	"digitalwallet/services/transfer/internal/repository"
	"digitalwallet/services/transfer/internal/walletclient"
	"digitalwallet/shared/identifiers"
)

type Service struct {
	repo                *repository.Repository
	source, destination *walletclient.Client
	maxAttempts         int
	timeout             time.Duration
}

func New(repo *repository.Repository, source, destination *walletclient.Client, maxAttempts int, timeout time.Duration) *Service {
	return &Service{repo: repo, source: source, destination: destination, maxAttempts: maxAttempts, timeout: timeout}
}

func (s *Service) Run(ctx context.Context, tenantID, transferID identifiers.UUID) error {
	t, err := s.repo.Get(ctx, tenantID, transferID)
	if err != nil {
		return err
	}
	steps, err := s.repo.Steps(ctx, tenantID, transferID)
	if err != nil {
		return err
	}
	if t.State == "COMPLETED" || t.State == "FAILED" || t.State == "COMPENSATED" || t.State == "RECONCILIATION_REQUIRED" {
		return nil
	}
	by := map[string]repository.Step{}
	for _, step := range steps {
		by[step.Name] = step
	}
	cmd := func() walletclient.Command {
		return walletclient.Command{TransferID: t.TransferID, AmountMinor: t.AmountMinor, Currency: t.Currency, Reference: t.Reference}
	}
	if err := s.doStep(ctx, tenantID, t.TransferID, by["RESERVE"], "RESERVING", s.source, t.SourceWalletID, "reservations", cmd()); err != nil {
		return err
	}
	if err := s.repo.SetState(ctx, tenantID, t.TransferID, "RESERVED"); err != nil {
		return err
	}
	if err := s.doStep(ctx, tenantID, t.TransferID, by["PENDING_CREDIT"], "PENDING_CREDIT", s.destination, t.DestinationWalletID, "pending-credits", cmd()); err != nil {
		if !walletclient.IsClass(err, walletclient.Ambiguous) {
			return s.compensateReserve(ctx, tenantID, t, by)
		}
		return err
	}
	if err := s.repo.SetState(ctx, tenantID, t.TransferID, "PENDING_CREDITED"); err != nil {
		return err
	}
	if err := s.doStep(ctx, tenantID, t.TransferID, by["CAPTURE"], "CAPTURING", s.source, t.SourceWalletID, "captures", cmd()); err != nil {
		if !walletclient.IsClass(err, walletclient.Ambiguous) {
			return s.compensateReserve(ctx, tenantID, t, by)
		}
		return err
	}
	if err := s.repo.SetState(ctx, tenantID, t.TransferID, "CAPTURED"); err != nil {
		return err
	}
	if err := s.doStep(ctx, tenantID, t.TransferID, by["FINALIZE"], "FINALIZING", s.destination, t.DestinationWalletID, "finalizations", cmd()); err != nil {
		if !walletclient.IsClass(err, walletclient.Ambiguous) {
			return s.compensateCapture(ctx, tenantID, t, by)
		}
		return err
	}
	return s.repo.SetState(ctx, tenantID, t.TransferID, "COMPLETED")
}

func (s *Service) compensateReserve(ctx context.Context, tenantID identifiers.UUID, t repository.Transfer, by map[string]repository.Step) error {
	_ = s.repo.SetState(ctx, tenantID, t.TransferID, "COMPENSATION_PENDING")
	cmd := walletclient.Command{
		TransferID:        t.TransferID,
		AmountMinor:       t.AmountMinor,
		Currency:          t.Currency,
		Reference:         t.Reference,
		TargetOperationID: by["RESERVE"].OperationID,
	}
	relStep := by["RELEASE"]
	if relStep.OperationID.IsZero() {
		relStep = repository.Step{Name: "RELEASE", OperationID: by["RESERVE"].OperationID}
	}
	err := s.doStep(ctx, tenantID, t.TransferID, relStep, "COMPENSATING", s.source, t.SourceWalletID, "releases", cmd)
	if err != nil {
		if !walletclient.IsClass(err, walletclient.Ambiguous) {
			if persistErr := s.persistReconciliation(ctx, tenantID, t.TransferID, "COMPENSATION_FAILED", map[string]any{"error": err.Error(), "stage": "release_reserve"}); persistErr != nil {
				return persistErr
			}
		}
		return err
	}
	return s.repo.SetState(ctx, tenantID, t.TransferID, "COMPENSATED")
}

func (s *Service) compensateCapture(ctx context.Context, tenantID identifiers.UUID, t repository.Transfer, by map[string]repository.Step) error {
	_ = s.repo.SetState(ctx, tenantID, t.TransferID, "COMPENSATION_PENDING")
	cmd := walletclient.Command{
		TransferID:        t.TransferID,
		AmountMinor:       t.AmountMinor,
		Currency:          t.Currency,
		Reference:         t.Reference,
		TargetOperationID: by["CAPTURE"].OperationID,
	}
	compStep := by["COMPENSATE"]
	if compStep.OperationID.IsZero() {
		compStep = repository.Step{Name: "COMPENSATE", OperationID: by["CAPTURE"].OperationID}
	}
	err := s.doStep(ctx, tenantID, t.TransferID, compStep, "COMPENSATING", s.source, t.SourceWalletID, "compensations", cmd)
	if err != nil {
		if !walletclient.IsClass(err, walletclient.Ambiguous) {
			if persistErr := s.persistReconciliation(ctx, tenantID, t.TransferID, "COMPENSATION_FAILED", map[string]any{"error": err.Error(), "stage": "compensate_capture"}); persistErr != nil {
				return persistErr
			}
		}
		return err
	}
	return s.repo.SetState(ctx, tenantID, t.TransferID, "COMPENSATED")
}

func (s *Service) doStep(ctx context.Context, tenantID, transferID identifiers.UUID, step repository.Step, state string, client *walletclient.Client, walletID identifiers.UUID, action string, command walletclient.Command) error {
	_ = state
	for attempt := 0; attempt < s.maxAttempts; attempt++ {
		if err := s.repo.BumpStep(ctx, tenantID, transferID, step.Name, "RUNNING", ""); err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, s.timeout)
		_, err := client.Do(callCtx, tenantID, walletID, step.OperationID, action, command)
		cancel()
		if err == nil {
			_ = s.repo.BumpStep(ctx, tenantID, transferID, step.Name, "COMPLETED", "")
			return nil
		}
		if walletclient.IsClass(err, walletclient.Ambiguous) {
			if persistErr := s.persistReconciliation(ctx, tenantID, transferID, step.Name, map[string]any{"operation_id": step.OperationID.String(), "error": err.Error()}); persistErr != nil {
				return persistErr
			}
			return err
		}
		if !walletclient.IsClass(err, walletclient.Transient) {
			_ = s.repo.BumpStep(ctx, tenantID, transferID, step.Name, "FAILED", err.Error())
			_ = s.repo.SetState(ctx, tenantID, transferID, "FAILED")
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
	return fmt.Errorf("step %s retries exhausted", step.Name)
}

func (s *Service) persistReconciliation(ctx context.Context, tenantID, transferID identifiers.UUID, category string, evidence any) error {
	if err := s.repo.SetState(ctx, tenantID, transferID, "RECONCILIATION_REQUIRED"); err != nil {
		return fmt.Errorf("persist reconciliation state: %w", err)
	}
	if _, _, err := s.repo.Reconciliation(ctx, tenantID, transferID, category, true, evidence); err != nil {
		return fmt.Errorf("persist reconciliation exception: %w", err)
	}
	return nil
}

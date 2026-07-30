package credit

import (
	"context"
	"fmt"
	"strings"

	"sub2api-ext/internal/store"
	"sub2api-ext/internal/sub2api"
)

const (
	SourceCheckin    = store.LedgerSourceCheckin
	SourceLottery    = store.LedgerSourceLottery
	SourceRankReward = store.LedgerSourceRankReward
	SourceTask       = store.LedgerSourceTask
)

type Request struct {
	UserID         int64
	Amount         float64
	Source         string
	SourceRef      string
	Scope          string
	Slot           string
	Notes          string
	IdempotencyKey string
}

type Result struct {
	LedgerID   int64
	NewBalance float64
	User       *sub2api.User
	IdemKey    string
	Skipped    bool
}

type Service struct {
	store  *store.Store
	client *sub2api.Client
}

func New(st *store.Store, client *sub2api.Client) *Service {
	return &Service{store: st, client: client}
}

func (s *Service) Grant(ctx context.Context, req Request) (*Result, error) {
	if req.UserID <= 0 {
		return nil, fmt.Errorf("user id required")
	}
	if req.Source == "" {
		return nil, fmt.Errorf("source required")
	}
	scope := req.Scope
	if scope == "" {
		scope = req.Source
	}
	slot := req.Slot
	if slot == "" {
		slot = "na"
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		key = sub2api.IdempotencyKey(scope, req.UserID, slot)
	}

	if ok, err := s.store.HasLedgerIdem(ctx, key); err != nil {
		return nil, err
	} else if ok {
		if existing, _ := s.store.GetLedgerByIdem(ctx, key); existing != nil {
			return &Result{LedgerID: existing.ID, IdemKey: key, Skipped: true}, nil
		}
		return &Result{IdemKey: key, Skipped: true}, nil
	}

	if req.Amount <= 0 {
		id, err := s.store.InsertLedger(ctx, store.LedgerEntry{
			UserID: req.UserID, Source: req.Source, SourceRef: req.SourceRef,
			Amount: 0, IdempotencyKey: key, Status: store.LedgerStatusSkipped, Notes: req.Notes,
		})
		if err != nil {
			return nil, err
		}
		return &Result{LedgerID: id, IdemKey: key, Skipped: true}, nil
	}

	user, err := s.client.AddBalanceScoped(ctx, scope, req.UserID, req.Amount, req.Notes, slot)
	if err != nil {
		user2, err2 := s.client.AddBalanceScoped(ctx, scope, req.UserID, req.Amount, req.Notes, slot)
		if err2 != nil {
			msg := strings.ToLower(err.Error() + " " + err2.Error())
			if strings.Contains(msg, "idempoten") || strings.Contains(msg, "duplicate") || strings.Contains(msg, "already") || strings.Contains(msg, "409") {
				if u3, e3 := s.client.GetUserByAdmin(ctx, req.UserID); e3 == nil {
					user = u3
					err = nil
				} else {
					_, _ = s.store.InsertLedger(ctx, store.LedgerEntry{
						UserID: req.UserID, Source: req.Source, SourceRef: req.SourceRef,
						Amount: req.Amount, IdempotencyKey: key, Status: store.LedgerStatusFailed,
						Notes: req.Notes, Error: err2.Error(),
					})
					return nil, err2
				}
			} else {
				_, _ = s.store.InsertLedger(ctx, store.LedgerEntry{
					UserID: req.UserID, Source: req.Source, SourceRef: req.SourceRef,
					Amount: req.Amount, IdempotencyKey: key, Status: store.LedgerStatusFailed,
					Notes: req.Notes, Error: err2.Error(),
				})
				return nil, err2
			}
		} else {
			user = user2
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	bal := 0.0
	if user != nil {
		bal = user.Balance
	}
	id, err := s.store.InsertLedger(ctx, store.LedgerEntry{
		UserID: req.UserID, Source: req.Source, SourceRef: req.SourceRef,
		Amount: req.Amount, IdempotencyKey: key, Status: store.LedgerStatusSuccess, Notes: req.Notes,
	})
	if err != nil {
		return &Result{User: user, NewBalance: bal, IdemKey: key}, fmt.Errorf("credited but ledger write failed: %w", err)
	}
	return &Result{LedgerID: id, User: user, NewBalance: bal, IdemKey: key}, nil
}

func (s *Service) Backfill(ctx context.Context) (int, error) {
	return s.store.BackfillLedgerFromLegacy(ctx)
}

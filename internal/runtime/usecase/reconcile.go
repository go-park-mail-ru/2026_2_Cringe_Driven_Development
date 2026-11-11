package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/google/uuid"
)

// reconcileLoop при старте и каждые ReconcileInterval до отмены ctx повторяет очистку CLEANUP_PENDING
// и ищет сирот. Просроченные lease возвращает в работу сам Claim, незавершённые окружения продолжают
// их задания.
func (w *Worker) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(w.s.ReconcileInterval)
	defer t.Stop()
	for {
		if err := w.reconcilePass(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("reconcile", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reconcilePass выполняет один проход reconcile с лимитом min(ReconcileInterval, 2 мин), чтобы
// зависший запрос к облаку или БД не остановил последующие проходы.
func (w *Worker) reconcilePass(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, min(w.s.ReconcileInterval, maxReconcilePass))
	defer cancel()
	return w.reconcile(ctx)
}

// reconcile ставит задание удаления каждому окружению в CLEANUP_PENDING (повтор очистки после
// ошибки удаления — не чаще раза за ReconcileInterval) и удаляет ресурсы рантайма, чья сессия неизвестна БД или уже DELETED/FAILED.
// ListOwned возвращает только ресурсы этого окружения (cellestial_env); ресурсы без session_id
// не трогаются.
//
// Ресурсы VPS пула помечены pool_id: без строки в runtime_pool они сироты. Строки пула разбирает
// reconcilePool.
func (w *Worker) reconcile(ctx context.Context) error {
	errs := w.retryCleanups(ctx)
	// Ресурсы читаются раньше строк пула: строка VPS пула создаётся до её ресурсов, поэтому у каждого
	// ресурса из списка строка уже есть, если VPS не удалена.
	owned, err := w.cloud.ListOwned(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list resources: %w", err))...)
	}
	var rows []domain.PoolVPS
	if w.pool != nil {
		if rows, err = w.pool.ListPool(ctx); err != nil {
			return errors.Join(append(errs, fmt.Errorf("list pool: %w", err))...)
		}
	}
	if err := w.deleteOrphans(ctx, owned, rows); err != nil {
		errs = append(errs, err)
	}
	if w.pool != nil {
		if err := w.reconcilePool(ctx, rows); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) retryCleanups(ctx context.Context) []error {
	pending, err := w.repo.ListByState(ctx, domain.StateCleanupPending)
	if err != nil {
		return []error{fmt.Errorf("list cleanup pending: %w", err)}
	}
	var errs []error
	for _, env := range pending {
		if err := w.repo.MarkForDelete(ctx, env.SessionID, "cleanup retry"); err != nil {
			errs = append(errs, fmt.Errorf("retry cleanup %s: %w", env.SessionID, err))
			continue
		}
		w.log.Info("cleanup retry scheduled", "session_id", env.SessionID)
	}
	return errs
}

func (w *Worker) deleteOrphans(ctx context.Context, owned []CloudResource, pool []domain.PoolVPS) error {
	var errs []error
	for _, kind := range deleteOrder() {
		for _, r := range owned {
			if r.Kind != kind || r.PoolID == uuid.Nil ||
				slices.ContainsFunc(pool, func(p domain.PoolVPS) bool { return p.ID == r.PoolID }) {
				continue
			}
			if err := w.deleteResource(ctx, r.Kind, r.ID); err != nil {
				errs = append(errs, fmt.Errorf("delete pool orphan %s %s: %w", r.Kind, r.ID, err))
				continue
			}
			w.log.Info("pool orphan deleted", "kind", r.Kind, "id", r.ID, "pool_id", r.PoolID)
		}
	}
	if err := w.deleteSessionOrphans(ctx, owned); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (w *Worker) deleteSessionOrphans(ctx context.Context, owned []CloudResource) error {
	var ids []uuid.UUID
	for _, r := range owned {
		if r.SessionID != uuid.Nil && !slices.Contains(ids, r.SessionID) {
			ids = append(ids, r.SessionID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	known, err := w.repo.KnownSessions(ctx, ids)
	if err != nil {
		return fmt.Errorf("known sessions: %w", err)
	}
	var errs []error
	for _, kind := range deleteOrder() {
		for _, r := range owned {
			if r.Kind != kind || r.SessionID == uuid.Nil {
				continue
			}
			if st, ok := known[r.SessionID]; ok && !st.Finished() {
				continue
			}
			if err := w.deleteResource(ctx, r.Kind, r.ID); err != nil {
				errs = append(errs, fmt.Errorf("delete orphan %s %s: %w", r.Kind, r.ID, err))
				continue
			}
			w.log.Info("orphan deleted", "kind", r.Kind, "id", r.ID, "session_id", r.SessionID)
		}
	}
	return errors.Join(errs...)
}

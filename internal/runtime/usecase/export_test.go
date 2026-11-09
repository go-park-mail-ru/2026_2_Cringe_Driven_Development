package usecase

import (
	"context"
	"time"
)

// SetVerifyInterval задаёт период опроса /health в тестах (по умолчанию 5 с).
func SetVerifyInterval(w *Worker, d time.Duration) { w.verifyInterval = d }

// Reconcile выполняет один проход поиска сирот.
func Reconcile(ctx context.Context, w *Worker) error { return w.reconcile(ctx) }

// SetHealthTimeout задаёт лимит одного вызова /health в тестах (по умолчанию 10 с).
func SetHealthTimeout(w *Worker, d time.Duration) { w.healthTimeout = d }

// SetAssignTimeout задаёт лимит одного вызова /assign в тестах (по умолчанию 1 мин).
func SetAssignTimeout(w *Worker, d time.Duration) { w.assignTimeout = d }

// ReconcilePass выполняет один проход reconcile с лимитом времени, как в фоновом цикле.
func ReconcilePass(ctx context.Context, w *Worker) error { return w.reconcilePass(ctx) }

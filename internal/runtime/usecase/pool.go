package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/google/uuid"
)

// Пул заранее подготовленных VPS (backend#89). Работает только при PoolSize > 0: тогда poolLoop держит
// PREPARING+READY = PoolSize, а запуск сессии забирает READY-VPS вместо создания новой. Reconcile
// удаляет устаревшие и лишние VPS пула и при PoolSize = 0 — все оставшиеся.

// claimPool в одной транзакции забирает READY-VPS пула для окружения QUEUED. Между захватом и /assign
// нет облака, SSH и Ansible: это путь пользователя. Токен сессии создаётся до захвата.
func (r *jobRun) claimPool(env domain.Environment) (bool, error) {
	if _, err := r.ensureSecrets(); err != nil {
		return false, err
	}
	got, ok, err := r.w.pool.ClaimPool(r.leaseCtx, env, r.w.s.Owner, r.w.s.ImageID, r.w.s.AgentVersion)
	if err != nil {
		return false, fmt.Errorf("claim pool vps: %w", err)
	}
	if !ok {
		r.w.log.Info("pool is empty, creating a new vps", "session_id", env.SessionID)
		return false, nil
	}
	r.w.log.Info("pool vps claimed", "session_id", env.SessionID, "pool_id", got.PoolID)
	r.w.kickPool()
	return true, nil
}

// leavePool: захваченная VPS пула не приняла назначение. Она удаляется, окружение без её ресурсов
// возвращается в CREATING и создаёт новую VPS в пределах прежнего StartupTimeout.
func (r *jobRun) leavePool(env domain.Environment, cause error) error {
	vps := domain.PoolVPS{ID: env.PoolID, Resources: env.Resources}
	r.w.log.Warn("pool vps rejected, creating a new vps", "session_id", env.SessionID, "pool_id", vps.ID, "error", cause)
	marked, err := r.w.pool.MarkPoolDeleting(r.leaseCtx, vps.ID, domain.PoolClaimed, env.SessionID, "assign failed: "+cause.Error())
	if err != nil {
		return err
	}
	env.Resources, env.PoolID = domain.Resources{}, uuid.Nil
	if _, err := r.save(env, domain.StateCreating); err != nil {
		return err
	}
	if !marked {
		return nil // VPS уже не этой сессии или удаляется: её ресурсы не трогаем
	}
	// Ошибка удаления не мешает сессии: строка DELETING остаётся, reconcile повторит.
	ctx, cancel := context.WithTimeout(r.leaseCtx, r.w.s.DeleteTimeout)
	defer cancel()
	r.w.deletePoolVPS(ctx, vps, "assign failed")
	return nil
}

// poolCheckInterval — период сверки пула с БД; запрос — один count под advisory-lock.
const poolCheckInterval = 30 * time.Second

func (w *Worker) kickPool() {
	select {
	case w.poolKick <- struct{}{}:
	default:
	}
}

// poolLoop пополняет пул при старте, по сигналу (свой захват, reconcile, конец backoff) и раз в
// poolCheckInterval: VPS мог захватить другой процесс (реплика API, runtime-smoke), чей сигнал сюда не
// доходит. Подготовка каждой VPS идёт в своей горутине и не занимает слоты заданий.
func (w *Worker) poolLoop(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(poolCheckInterval)
	defer t.Stop()
	for {
		w.replenish(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-w.poolKick:
		case <-t.C:
		}
	}
}

func (w *Worker) replenish(ctx context.Context, wg *sync.WaitGroup) {
	w.poolMu.Lock()
	paused := w.clock.Now().Before(w.poolRetryAt)
	w.poolMu.Unlock()
	if paused {
		return
	}
	added, err := w.pool.ReservePool(ctx, w.s.PoolSize, w.s.ImageID, w.s.AgentVersion)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("reserve pool vps", "error", err)
		}
		return
	}
	for _, vps := range added {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.preparePool(ctx, vps)
		}()
	}
}

// preparePool создаёт VPS пула, запускает prepare.yml и ждёт агента в режиме ожидания. Сбой удаляет
// VPS и откладывает следующую подготовку с backoff; сессий он не касается.
func (w *Worker) preparePool(ctx context.Context, vps domain.PoolVPS) {
	w.log.Info("pool vps preparing", "pool_id", vps.ID)
	pctx, cancel := context.WithTimeout(ctx, w.s.StartupTimeout)
	err := w.preparePoolSteps(pctx, &vps)
	cancel()
	if err == nil {
		vps.State = domain.PoolReady
		if err = w.pool.SavePreparing(ctx, vps); err == nil {
			w.log.Info("pool vps ready", "pool_id", vps.ID, "private_ip", vps.Resources.PrivateIP)
			w.poolMu.Lock()
			w.poolFailures, w.poolRetryAt = 0, time.Time{}
			w.poolMu.Unlock()
			return
		}
	}
	// Остановка API: VPS помечается к удалению, ресурсы удалит reconcile следующего запуска. Не удалось
	// пометить — VPS уже в DELETING (её забрал reconcile), удаление за ним.
	mctx, mcancel := context.WithTimeout(context.WithoutCancel(ctx), w.s.DrainTimeout)
	defer mcancel()
	marked, merr := w.pool.MarkPoolDeleting(mctx, vps.ID, domain.PoolPreparing, uuid.Nil, "preparation failed: "+err.Error())
	if merr != nil {
		w.log.Error("mark pool vps deleting", "pool_id", vps.ID, "error", merr)
	}
	if ctx.Err() != nil || !marked {
		return
	}
	w.poolMu.Lock()
	w.poolFailures++
	delay := backoff(w.poolFailures)
	w.poolRetryAt = w.clock.Now().Add(delay)
	w.poolMu.Unlock()
	w.log.Warn("pool vps preparation failed", "pool_id", vps.ID, "error", err, "retry_in", delay)
	dctx, dcancel := context.WithTimeout(ctx, w.s.DeleteTimeout)
	defer dcancel()
	w.deletePoolVPS(dctx, vps, "preparation failed")
	time.AfterFunc(delay, w.kickPool)
}

// preparePoolSteps — те же шаги, что у новой VPS сессии: host key, порт, том, сервер, prepare.yml,
// затем /health агента без токена до состояния WAITING с готовым kernel. ID ресурсов сохраняются сразу.
func (w *Worker) preparePoolSteps(ctx context.Context, vps *domain.PoolVPS) error {
	save := func() error { return w.pool.SavePreparing(ctx, *vps) }
	priv, pub, err := w.sec.NewHostKey()
	if err != nil {
		return fmt.Errorf("new host key: %w", err)
	}
	if vps.HostPrivateKey, err = w.sec.Seal(priv); err != nil {
		return fmt.Errorf("seal host key: %w", err)
	}
	vps.HostPublicKey = pub
	if err = save(); err != nil {
		return err
	}
	portID, ip, cerr := w.cloud.EnsurePort(ctx, vps.Env())
	if portID != "" {
		vps.Resources.PortID, vps.Resources.PrivateIP = portID, ip
		if err = save(); err != nil {
			return err
		}
	}
	if cerr != nil {
		return fmt.Errorf("ensure port: %w", cerr)
	}
	volumeID, cerr := w.cloud.EnsureVolume(ctx, vps.Env())
	if volumeID != "" {
		vps.Resources.VolumeID = volumeID
		if err = save(); err != nil {
			return err
		}
	}
	if cerr != nil {
		return fmt.Errorf("ensure volume: %w", cerr)
	}
	serverID, cerr := w.cloud.EnsureServer(ctx, vps.Env(), cloudInit(priv, pub))
	if serverID != "" {
		vps.Resources.ServerID = serverID
		if err = save(); err != nil {
			return err
		}
	}
	if cerr != nil {
		return fmt.Errorf("ensure server: %w", cerr)
	}
	if err = w.prepareHost(ctx, vps.ID, vps.Resources.PrivateIP, pub); err != nil {
		return err
	}
	return w.waitWaiting(ctx, vps.Resources.PrivateIP)
}

// waitWaiting опрашивает /health агента без токена, пока он не ответит WAITING с готовым kernel.
func (w *Worker) waitWaiting(ctx context.Context, ip string) error {
	ep := AgentEndpoint{BaseURL: agentURL(ip)}
	for {
		hctx, cancel := context.WithTimeout(ctx, w.healthTimeout)
		h, status, err := w.agent.Health(hctx, ep)
		cancel()
		if err == nil && status == httpStatusOK && h.State == "WAITING" && h.Ready {
			return nil
		}
		if serr := sleepCtx(ctx, w.verifyInterval); serr != nil {
			return fmt.Errorf("agent is not waiting (HTTP %d, state %q): %w", status, h.State, errors.Join(serr, err))
		}
	}
}

// deletePoolVPS удаляет ресурсы VPS пула — известные по ID и найденные по pool_id — и её строку.
// Ошибка остаётся в логе: строка в DELETING, reconcile повторит.
func (w *Worker) deletePoolVPS(ctx context.Context, vps domain.PoolVPS, reason string) {
	if err := w.deletePoolResources(ctx, vps); err != nil {
		w.log.Warn("pool vps cleanup failed", "pool_id", vps.ID, "reason", reason, "error", err)
		return
	}
	if err := w.pool.DeletePool(ctx, vps.ID); err != nil {
		w.log.Warn("delete pool row", "pool_id", vps.ID, "error", err)
		return
	}
	w.log.Info("pool vps deleted", "pool_id", vps.ID, "reason", reason)
}

func (w *Worker) deletePoolResources(ctx context.Context, vps domain.PoolVPS) error {
	owned, err := w.cloud.ListOwned(ctx)
	if err != nil {
		return fmt.Errorf("list resources: %w", err)
	}
	ids := map[string][]string{
		kindServer: nonEmpty(vps.Resources.ServerID),
		kindVolume: nonEmpty(vps.Resources.VolumeID),
		kindPort:   nonEmpty(vps.Resources.PortID),
	}
	for _, res := range owned {
		if res.PoolID == vps.ID && !slices.Contains(ids[res.Kind], res.ID) {
			ids[res.Kind] = append(ids[res.Kind], res.ID)
		}
	}
	for _, kind := range deleteOrder() {
		for _, id := range ids[kind] {
			if err := w.deleteResource(ctx, kind, id); err != nil {
				return fmt.Errorf("delete %s %s: %w", kind, id, err)
			}
		}
	}
	return nil
}

// reconcilePool удаляет VPS пула: в DELETING; устаревшие (другие образ или версия агента); зависшие в
// PREPARING дольше StartupTimeout (процесс подготовки упал); сверх PoolSize (при 0 — все); CLAIMED,
// чья сессия уже завершена (ресурсы удалила сессия, строка больше не нужна). Затем пополняет пул.
func (w *Worker) reconcilePool(ctx context.Context, rows []domain.PoolVPS) error {
	known, err := w.claimedSessions(ctx, rows)
	if err != nil {
		return err
	}
	current := 0
	for _, vps := range rows {
		reason := ""
		switch vps.State {
		case domain.PoolDeleting:
			reason = "cleanup retry"
		case domain.PoolClaimed:
			if st, ok := known[vps.SessionID]; !ok || st.Finished() {
				reason = "session finished"
			}
		case domain.PoolPreparing, domain.PoolReady:
			switch {
			case vps.ImageID != w.s.ImageID || vps.AgentVersion != w.s.AgentVersion:
				reason = "stale image or agent version"
			case vps.State == domain.PoolPreparing && !w.clock.Now().Before(vps.CreatedAt.Add(w.s.StartupTimeout)):
				reason = "preparation abandoned"
			case current >= w.s.PoolSize:
				reason = "pool size reduced"
			default:
				current++
			}
		}
		if reason == "" {
			continue
		}
		// Решение принято по снимку: VPS могли захватить или удалить после него. Ресурсы удаляются
		// только после перехода из того же состояния, что в снимке.
		if vps.State != domain.PoolDeleting {
			marked, err := w.pool.MarkPoolDeleting(ctx, vps.ID, vps.State, vps.SessionID, reason)
			if err != nil {
				return err
			}
			if !marked {
				continue
			}
		}
		w.deletePoolVPS(ctx, vps, reason)
	}
	if w.s.PoolSize > 0 {
		w.kickPool()
	}
	return nil
}

func (w *Worker) claimedSessions(ctx context.Context, rows []domain.PoolVPS) (map[uuid.UUID]domain.State, error) {
	var ids []uuid.UUID
	for _, vps := range rows {
		if vps.State == domain.PoolClaimed {
			ids = append(ids, vps.SessionID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	known, err := w.repo.KnownSessions(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("known sessions: %w", err)
	}
	return known, nil
}

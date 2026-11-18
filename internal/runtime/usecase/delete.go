package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
)

// Виды облачных ресурсов в CloudResource.Kind, в порядке удаления.
const (
	kindServer = "server"
	kindVolume = "volume"
	kindPort   = "port"
)

// deleteOrder — порядок удаления: сервер, затем том, затем порт.
func deleteOrder() [3]string { return [3]string{kindServer, kindVolume, kindPort} }

// deleteStep выполняет удаление: DELETING → drain/stop агента → server, volume, port → DELETED.
//
// Правило итога: CLEANUP_PENDING с непустым last_error — очистка после ошибки создания
// (last_error пишет только createFailed, READY его сбрасывает); она идёт без перехода в DELETING
// и завершается FAILED. Любое другое удаление (запрос пользователя, повтор после ошибки удаления,
// у которой причина в cleanup_result) идёт через DELETING и завершается DELETED. desired_state для
// различения не годится: повтор очистки из reconcile ставит его в DELETED через MarkForDelete.
func (r *jobRun) deleteStep(env domain.Environment) (bool, error) {
	if env.State.Finished() {
		return r.finish()
	}
	failedProvisioning := env.State == domain.StateCleanupPending && env.LastError != ""
	if !failedProvisioning && env.State != domain.StateDeleting {
		env.Endpoint = ""
		_, err := r.save(env, domain.StateDeleting)
		return false, err
	}
	r.drain(env)
	env, err := r.deleteResources(env)
	if err != nil {
		return false, err
	}
	final := domain.StateDeleted
	if failedProvisioning {
		final = domain.StateFailed
	}
	env.Endpoint = ""
	env.CleanupResult = "all session resources deleted"
	if _, err := r.save(env, final); err != nil {
		return false, err
	}
	r.w.log.Info("environment cleaned up", "session_id", env.SessionID, "state", final)
	return r.finish()
}

// drain просит агента завершить работу; недоступность агента удаление не блокирует.
func (r *jobRun) drain(env domain.Environment) {
	if r.drained || env.Resources.ServerID == "" || env.Resources.PrivateIP == "" {
		return
	}
	r.drained = true
	ctx, cancel := context.WithTimeout(r.leaseCtx, r.w.s.DrainTimeout)
	defer cancel()
	s, err := r.w.repo.LoadSecrets(ctx, r.job.SessionID)
	if err != nil || len(s.Token) == 0 {
		return
	}
	ep, err := r.agentEndpoint(env, s)
	if err != nil {
		return
	}
	if err := r.w.agent.Drain(ctx, ep); err != nil {
		r.w.log.Info("agent drain skipped", "session_id", env.SessionID, "error", err)
		return
	}
	if err := r.w.agent.Stop(ctx, ep); err != nil {
		r.w.log.Info("agent stop failed", "session_id", env.SessionID, "error", err)
	}
}

// deleteResources удаляет ресурсы сессии — известные по ID из БД и найденные по metadata —
// и сохраняет окружение после удаления каждого вида.
func (r *jobRun) deleteResources(env domain.Environment) (domain.Environment, error) {
	ctx, cancel := context.WithTimeout(r.leaseCtx, r.w.s.DeleteTimeout)
	defer cancel()
	owned, err := r.w.cloud.ListOwned(ctx)
	if err != nil {
		return env, fmt.Errorf("list resources: %w", err)
	}
	ids := map[string][]string{
		kindServer: nonEmpty(env.Resources.ServerID),
		kindVolume: nonEmpty(env.Resources.VolumeID),
		kindPort:   nonEmpty(env.Resources.PortID),
	}
	for _, res := range owned {
		if res.SessionID == env.SessionID && !slices.Contains(ids[res.Kind], res.ID) {
			ids[res.Kind] = append(ids[res.Kind], res.ID)
		}
	}
	for _, kind := range deleteOrder() {
		for _, id := range ids[kind] {
			if derr := r.w.deleteResource(ctx, kind, id); derr != nil {
				return env, fmt.Errorf("delete %s %s: %w", kind, id, derr)
			}
		}
		before := env.Resources
		switch kind {
		case kindServer:
			env.Resources.ServerID = ""
		case kindVolume:
			env.Resources.VolumeID = ""
		case kindPort:
			env.Resources.PortID, env.Resources.PrivateIP = "", ""
		}
		if env.Resources != before {
			if env, err = r.save(env, env.State); err != nil {
				return env, err
			}
		}
	}
	return env, nil
}

// deleteResource запрашивает удаление известного ресурса окружения.
func (w *Worker) deleteResource(ctx context.Context, kind, id string) error {
	switch kind {
	case kindServer:
		return w.cloud.DeleteServer(ctx, id)
	case kindVolume:
		return w.cloud.DeleteVolume(ctx, id)
	case kindPort:
		return w.cloud.DeletePort(ctx, id)
	}
	return fmt.Errorf("unknown resource kind %q", kind)
}

// deleteFailed: постоянная ошибка облака → CLEANUP_PENDING с причиной в cleanup_result и
// завершение задания без повтора; иначе — повтор с backoff.
func (r *jobRun) deleteFailed(cause error) (bool, error) {
	if !errors.Is(cause, ErrCloudPermanent) {
		return true, r.retry(cause)
	}
	env, err := r.w.repo.Get(r.leaseCtx, r.job.SessionID)
	if err != nil {
		return true, fmt.Errorf("get environment: %w", err)
	}
	env.CleanupResult = "cleanup failed: " + cause.Error()
	r.w.log.Error("cleanup failed", "session_id", env.SessionID, "error", cause)
	if _, err := r.save(env, domain.StateCleanupPending); err != nil {
		if errors.Is(err, ErrStale) {
			return false, nil
		}
		return true, err
	}
	if err := r.complete(); err != nil {
		if errors.Is(err, ErrStale) {
			return false, nil
		}
		return true, err
	}
	return true, nil
}

// nonEmpty выбирает доступное непустое значение для очистки.
func nonEmpty(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

package usecase

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/google/uuid"
)

// createStep выполняет один шаг создания по env.State и сохраняет результат.
func (r *jobRun) createStep(env domain.Environment) (bool, error) {
	// READY только завершает задание: таймаут старта к готовому окружению не относится.
	if provisioning(env.State) && r.startupExpired(env) {
		return false, errStartupTimeout
	}
	ctx, cancel := r.startupCtx(env)
	defer cancel()
	switch env.State {
	case domain.StateQueued:
		if r.w.s.PoolSize > 0 {
			if claimed, err := r.claimPool(env); claimed || err != nil {
				return false, err
			}
		}
		_, err := r.save(env, domain.StateCreating)
		return false, err
	case domain.StateCreating:
		return false, startupErr(ctx, r.provision(ctx, env))
	case domain.StateConfiguring:
		if err := r.configure(ctx, env); err != nil {
			return false, startupErr(ctx, err)
		}
		_, err := r.save(env, domain.StateVerifying)
		return false, err
	case domain.StateVerifying:
		if err := r.assign(ctx, env); err != nil {
			if env.PoolID != uuid.Nil && ctx.Err() == nil {
				return false, r.leavePool(env, err)
			}
			return false, startupErr(ctx, err)
		}
		env.Endpoint = agentURL(env.Resources.PrivateIP)
		env.LastError = ""
		if _, err := r.save(env, domain.StateReady); err != nil {
			return false, err
		}
		return r.finish()
	case domain.StateReady:
		return r.finish()
	default:
		return false, fmt.Errorf("unexpected state %s for create", env.State)
	}
}

// provisioning — состояния, из которых ошибка создания переводит в CLEANUP_PENDING. Из QUEUED
// такого перехода нет (ресурсов ещё нет), READY уже готово.
func provisioning(s domain.State) bool {
	return s == domain.StateCreating || s == domain.StateConfiguring || s == domain.StateVerifying
}

func (r *jobRun) startupExpired(env domain.Environment) bool {
	return !r.w.clock.Now().Before(env.CreatedAt.Add(r.w.s.StartupTimeout))
}

// startupCtx — createCtx с крайним сроком CreatedAt+StartupTimeout. Остаток считается по Clock
// воркера; истечение срока отменяет контекст с причиной errStartupTimeout, а потеря lease и запрос
// удаления по-прежнему видны как причины createCtx.
func (r *jobRun) startupCtx(env domain.Environment) (context.Context, context.CancelFunc) {
	left := env.CreatedAt.Add(r.w.s.StartupTimeout).Sub(r.w.clock.Now())
	return context.WithTimeoutCause(r.createCtx, left, errStartupTimeout)
}

// startupErr помечает ошибку шага errStartupTimeout, если шаг прервал крайний срок старта
// (а не потеря lease или запрос удаления): createFailed тогда переводит окружение в очистку.
func startupErr(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, errStartupTimeout) || !errors.Is(context.Cause(ctx), errStartupTimeout) {
		return err
	}
	return fmt.Errorf("%w: %w", errStartupTimeout, err)
}

// provision создаёт порт, том и сервер; каждый полученный ID сохраняется сразу, даже вместе
// с ошибкой облака, чтобы повтор и удаление нашли ресурс. Ensure* вызывается и для известного ID:
// первое ожидание готовности могло оборваться, а следующий шаг требует готовый ресурс.
func (r *jobRun) provision(ctx context.Context, env domain.Environment) error {
	var err error
	portID, ip, cerr := r.w.cloud.EnsurePort(ctx, env)
	if portID != "" && (portID != env.Resources.PortID || ip != env.Resources.PrivateIP) {
		env.Resources.PortID, env.Resources.PrivateIP = portID, ip
		if env, err = r.save(env, env.State); err != nil {
			return err
		}
	}
	if cerr != nil {
		return fmt.Errorf("ensure port: %w", cerr)
	}
	volumeID, cerr := r.w.cloud.EnsureVolume(ctx, env)
	if volumeID != "" && volumeID != env.Resources.VolumeID {
		env.Resources.VolumeID = volumeID
		if env, err = r.save(env, env.State); err != nil {
			return err
		}
	}
	if cerr != nil {
		return fmt.Errorf("ensure volume: %w", cerr)
	}
	// Host key генерируется и сохраняется до создания сервера: повтор берёт тот же ключ.
	s, serr := r.ensureSecrets()
	if serr != nil {
		return serr
	}
	priv, oerr := r.w.sec.Open(s.HostPrivateKey)
	if oerr != nil {
		return fmt.Errorf("open host key: %w", oerr)
	}
	serverID, cerr := r.w.cloud.EnsureServer(ctx, env, cloudInit(priv, s.HostPublicKey))
	if serverID != "" && serverID != env.Resources.ServerID {
		env.Resources.ServerID = serverID
		if env, err = r.save(env, env.State); err != nil {
			return err
		}
	}
	if cerr != nil {
		return fmt.Errorf("ensure server: %w", cerr)
	}
	_, err = r.save(env, domain.StateConfiguring)
	return err
}

// ensureSecrets загружает секреты окружения, создавая недостающие host key и токен.
func (r *jobRun) ensureSecrets() (SealedSecrets, error) {
	s, err := r.w.repo.LoadSecrets(r.leaseCtx, r.job.SessionID)
	if err != nil {
		return SealedSecrets{}, fmt.Errorf("load secrets: %w", err)
	}
	changed := false
	if len(s.HostPrivateKey) == 0 || s.HostPublicKey == "" {
		priv, pub, err := r.w.sec.NewHostKey()
		if err != nil {
			return SealedSecrets{}, fmt.Errorf("new host key: %w", err)
		}
		if s.HostPrivateKey, err = r.w.sec.Seal(priv); err != nil {
			return SealedSecrets{}, fmt.Errorf("seal host key: %w", err)
		}
		s.HostPublicKey, changed = pub, true
	}
	if len(s.Token) == 0 {
		tok, err := r.w.sec.NewToken()
		if err != nil {
			return SealedSecrets{}, fmt.Errorf("new token: %w", err)
		}
		if s.Token, err = r.w.sec.Seal([]byte(tok)); err != nil {
			return SealedSecrets{}, fmt.Errorf("seal token: %w", err)
		}
		changed = true
	}
	if changed {
		if err := r.w.repo.SaveSecrets(r.leaseCtx, r.job.SessionID, s, r.w.s.Owner); err != nil {
			return SealedSecrets{}, fmt.Errorf("save secrets: %w", err)
		}
	}
	return s, nil
}

// cloudInit — user_data с заранее сгенерированным host key ed25519.
func cloudInit(privatePEM []byte, publicKey string) string {
	var b strings.Builder
	b.WriteString("#cloud-config\nssh_deletekeys: true\nssh_keys:\n  ed25519_private: |\n")
	for _, line := range strings.Split(strings.TrimRight(string(privatePEM), "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	b.WriteString("  ed25519_public: " + strconv.Quote(publicKey) + "\n")
	return b.String()
}

func agentURL(ip string) string { return "https://" + ip + ":" + agentPort }

func (r *jobRun) agentEndpoint(env domain.Environment, s SealedSecrets) (AgentEndpoint, error) {
	tok, err := r.w.sec.Open(s.Token)
	if err != nil {
		return AgentEndpoint{}, fmt.Errorf("open token: %w", err)
	}
	return AgentEndpoint{BaseURL: agentURL(env.Resources.PrivateIP), Token: string(tok)}, nil
}

// configure готовит новую VPS сессии (prepare.yml) с лимитом AnsibleTimeout.
func (r *jobRun) configure(ctx context.Context, env domain.Environment) error {
	s, err := r.ensureSecrets()
	if err != nil {
		return err
	}
	return r.w.prepareHost(ctx, env.SessionID, env.Resources.PrivateIP, s.HostPublicKey)
}

// prepareHost запускает prepare.yml на VPS: агент стартует в режиме ожидания, данных сессии нет.
func (w *Worker) prepareHost(ctx context.Context, name uuid.UUID, ip, hostPublicKey string) error {
	certPEM, keyPEM, err := w.sec.IssueServerCert(ip)
	if err != nil {
		return fmt.Errorf("issue server cert: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, w.s.AnsibleTimeout)
	defer cancel()
	err = w.host.Configure(ctx, HostConfig{
		Name: name, PrivateIP: ip, HostPublicKey: hostPublicKey, ServerCertPEM: certPEM, ServerKeyPEM: keyPEM,
		PythonImage: w.s.PythonImage, KernelDNS: w.s.KernelDNS, BlockedPublicIPs: w.s.BlockedPublicIPs,
	})
	if err != nil {
		return fmt.Errorf("configure host: %w", err)
	}
	return nil
}

// assign назначает агента сессии (POST /assign) и ждёт ответа о готовности её kernel. Новая VPS
// повторяется до StartupTimeout: агент мог ещё не подняться. VPS пула не повторяется: её агент уже
// проверен, и сбой — повод взять новую VPS (leavePool). Отказ агента (400/409) — постоянная ошибка.
func (r *jobRun) assign(ctx context.Context, env domain.Environment) error {
	s, err := r.w.repo.LoadSecrets(r.leaseCtx, r.job.SessionID)
	if err != nil {
		return fmt.Errorf("load secrets: %w", err)
	}
	ep, err := r.agentEndpoint(env, s)
	if err != nil {
		return err
	}
	for {
		actx, cancel := context.WithTimeout(ctx, r.w.assignTimeout)
		h, status, aerr := r.w.agent.Assign(actx, ep, env.SessionID, env.Language)
		cancel()
		reason := healthProblem(env, h, status, aerr)
		switch {
		case reason == "":
			return nil
		case status == httpStatusBadRequest || status == httpStatusConflict:
			return fmt.Errorf("%w: %s", errAssignRejected, reason)
		case env.PoolID != uuid.Nil:
			return fmt.Errorf("pool vps: %s", reason)
		}
		if ctx.Err() != nil {
			if errors.Is(context.Cause(ctx), errStartupTimeout) {
				return fmt.Errorf("%w: agent is not assigned: %s", errStartupTimeout, reason)
			}
			return context.Cause(ctx)
		}
		if r.startupExpired(env) {
			return fmt.Errorf("%w: agent is not assigned: %s", errStartupTimeout, reason)
		}
		if err := sleepCtx(ctx, r.w.verifyInterval); err != nil {
			return err
		}
	}
}

func healthProblem(env domain.Environment, h AgentHealth, status int, err error) string {
	switch {
	case err != nil:
		return "agent: " + err.Error()
	case status != httpStatusOK:
		return "agent: HTTP " + strconv.Itoa(status)
	case h.SessionID != env.SessionID.String():
		return "agent: foreign session " + h.SessionID
	case h.Language != env.Language:
		return "agent: language " + h.Language + ", want " + env.Language
	case !h.Ready:
		return "agent: state " + h.State
	}
	return ""
}

// createFailed обрабатывает ошибку шага создания. В CREATING/CONFIGURING/VERIFYING постоянная
// ошибка или истёкший StartupTimeout → last_error и CLEANUP_PENDING, и цикл того же задания сразу
// продолжает удалением (итог FAILED). Иначе — повтор задания с backoff.
func (r *jobRun) createFailed(cause error) (bool, error) {
	env, err := r.w.repo.Get(r.leaseCtx, r.job.SessionID)
	if err != nil {
		return true, fmt.Errorf("get environment: %w", err)
	}
	if provisioning(env.State) && (errors.Is(cause, ErrCloudPermanent) || errors.Is(cause, errAssignRejected) ||
		errors.Is(cause, errStartupTimeout) || r.startupExpired(env)) {
		env.LastError = "provisioning failed: " + cause.Error()
		r.w.log.Warn("provisioning failed", "session_id", env.SessionID, "state", env.State, "error", cause)
		_, err := r.save(env, domain.StateCleanupPending)
		if err != nil && !errors.Is(err, ErrStale) {
			return true, err
		}
		return false, nil
	}
	return true, r.retry(cause)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}

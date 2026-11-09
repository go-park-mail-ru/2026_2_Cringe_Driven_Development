package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/domain"
	"github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtime/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	poolLockSQL  = `SELECT pg_advisory_xact_lock(hashtext('runtime_pool'))`
	poolCountSQL = `SELECT count(*) FROM runtime_pool
		WHERE state IN ('PREPARING','READY') AND image_id = $1 AND agent_version = $2`
	poolInsertSQL = `INSERT INTO runtime_pool (id, state, image_id, agent_version) VALUES ($1, 'PREPARING', $2, $3)
		RETURNING created_at, updated_at`
	poolSaveSQL = `UPDATE runtime_pool SET state = $2, server_id = NULLIF($3, ''), volume_id = NULLIF($4, ''),
		port_id = NULLIF($5, ''), private_ip = NULLIF($6, ''), host_private_key_sealed = $7, host_public_key = NULLIF($8, ''),
		updated_at = now()
		WHERE id = $1 AND state = 'PREPARING'`
	poolColumns = `id, state, COALESCE(server_id, ''), COALESCE(volume_id, ''), COALESCE(port_id, ''),
		COALESCE(private_ip, ''), host_private_key_sealed, COALESCE(host_public_key, ''), image_id, agent_version,
		COALESCE(session_id, '00000000-0000-0000-0000-000000000000'), COALESCE(last_error, ''), created_at, updated_at`
	poolListSQL      = `SELECT ` + poolColumns + ` FROM runtime_pool ORDER BY created_at, id`
	poolPickReadySQL = `SELECT ` + poolColumns + ` FROM runtime_pool
		WHERE state = 'READY' AND image_id = $1 AND agent_version = $2
		ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`
	poolClaimSQL   = `UPDATE runtime_pool SET state = 'CLAIMED', session_id = $2, updated_at = now() WHERE id = $1`
	envFromPoolSQL = `UPDATE runtime_environments e SET state = 'VERIFYING', pool_id = $3, server_id = $4,
		volume_id = $5, port_id = $6, private_ip = $7, host_private_key_sealed = $8, host_public_key = $9,
		version = e.version + 1, updated_at = now()
		WHERE e.session_id = $1 AND e.version = $2 AND e.state = 'QUEUED' AND EXISTS (
			SELECT 1 FROM runtime_jobs j WHERE j.session_id = e.session_id AND j.status = 'running' AND j.lease_owner = $10)
		RETURNING e.version, e.updated_at`
	poolDeletingSQL = `UPDATE runtime_pool SET state = 'DELETING', last_error = $2, updated_at = now()
		WHERE id = $1 AND state <> 'DELETING'`
	poolDeleteSQL = `DELETE FROM runtime_pool WHERE id = $1`
)

func scanPool(row pgx.Row) (domain.PoolVPS, error) {
	var p domain.PoolVPS
	var state string
	err := row.Scan(&p.ID, &state, &p.Resources.ServerID, &p.Resources.VolumeID, &p.Resources.PortID,
		&p.Resources.PrivateIP, &p.HostPrivateKey, &p.HostPublicKey, &p.ImageID, &p.AgentVersion, &p.SessionID,
		&p.LastError, &p.CreatedAt, &p.UpdatedAt)
	p.State = domain.PoolState(state)
	return p, err
}

// ReservePool добавляет недостающие до size VPS пула в PREPARING под advisory-lock пула.
func (r *Repository) ReservePool(ctx context.Context, size int, imageID, agentVersion string) ([]domain.PoolVPS, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, poolLockSQL); err != nil {
		return nil, fmt.Errorf("pool lock: %w", err)
	}
	var have int
	if err = tx.QueryRow(ctx, poolCountSQL, imageID, agentVersion).Scan(&have); err != nil {
		return nil, fmt.Errorf("count pool: %w", err)
	}
	var out []domain.PoolVPS
	for range size - have {
		p := domain.PoolVPS{ID: uuid.New(), State: domain.PoolPreparing, ImageID: imageID, AgentVersion: agentVersion}
		if err = tx.QueryRow(ctx, poolInsertSQL, p.ID, imageID, agentVersion).Scan(&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("insert pool vps: %w", err)
		}
		out = append(out, p)
	}
	return out, commit(ctx, tx)
}

// SavePreparing сохраняет VPS в PREPARING; VPS уже в другом состоянии → ErrStale.
func (r *Repository) SavePreparing(ctx context.Context, p domain.PoolVPS) error {
	tag, err := r.db.Exec(ctx, poolSaveSQL, p.ID, string(p.State), p.Resources.ServerID, p.Resources.VolumeID,
		p.Resources.PortID, p.Resources.PrivateIP, p.HostPrivateKey, p.HostPublicKey)
	if err != nil {
		return fmt.Errorf("save pool vps: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return usecase.ErrStale
	}
	return nil
}

// ClaimPool переносит READY-VPS в окружение в одной транзакции; одна VPS достаётся одной сессии.
func (r *Repository) ClaimPool(ctx context.Context, env domain.Environment, owner, imageID, agentVersion string) (domain.Environment, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return env, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	p, err := scanPool(tx.QueryRow(ctx, poolPickReadySQL, imageID, agentVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return env, false, nil
	}
	if err != nil {
		return env, false, fmt.Errorf("pick pool vps: %w", err)
	}
	err = tx.QueryRow(ctx, envFromPoolSQL, env.SessionID, env.Version, p.ID, p.Resources.ServerID,
		p.Resources.VolumeID, p.Resources.PortID, p.Resources.PrivateIP, p.HostPrivateKey, p.HostPublicKey, owner,
	).Scan(&env.Version, &env.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return env, false, usecase.ErrStale
	}
	if err != nil {
		return env, false, fmt.Errorf("move pool vps to environment: %w", err)
	}
	if _, err = tx.Exec(ctx, poolClaimSQL, p.ID, env.SessionID); err != nil {
		return env, false, fmt.Errorf("claim pool vps: %w", err)
	}
	if err = commit(ctx, tx); err != nil {
		return env, false, err
	}
	env.State, env.PoolID, env.Resources = domain.StateVerifying, p.ID, p.Resources
	return env, true, nil
}

// MarkPoolDeleting переводит VPS в DELETING.
func (r *Repository) MarkPoolDeleting(ctx context.Context, id uuid.UUID, reason string) error {
	if _, err := r.db.Exec(ctx, poolDeletingSQL, id, reason); err != nil {
		return fmt.Errorf("mark pool vps deleting: %w", err)
	}
	return nil
}

// ListPool возвращает все VPS пула, старые первыми.
func (r *Repository) ListPool(ctx context.Context) ([]domain.PoolVPS, error) {
	rows, err := r.db.Query(ctx, poolListSQL)
	if err != nil {
		return nil, fmt.Errorf("select pool: %w", err)
	}
	defer rows.Close()
	var out []domain.PoolVPS
	for rows.Next() {
		p, serr := scanPool(rows)
		if serr != nil {
			return nil, fmt.Errorf("scan pool vps: %w", serr)
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pool: %w", err)
	}
	return out, nil
}

// DeletePool удаляет строку VPS пула.
func (r *Repository) DeletePool(ctx context.Context, id uuid.UUID) error {
	if _, err := r.db.Exec(ctx, poolDeleteSQL, id); err != nil {
		return fmt.Errorf("delete pool vps: %w", err)
	}
	return nil
}

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	D "github.com/go-park-mail-ru/2026_2_Cringe_Driven_Development/internal/runtimesession/domain"
	"github.com/jackc/pgx/v5"
)

// displayKey получает хэш display_id, чтобы индекс поддерживал любые символы исходного ID.
func displayKey(raw json.RawMessage) (string, error) {
	var message struct {
		Transient struct {
			ID string `json:"display_id"`
		} `json:"transient"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return "", err
	}
	if message.Transient.ID == "" {
		return "", nil
	}
	hash := sha256.Sum256([]byte(message.Transient.ID))
	return hex.EncodeToString(hash[:]), nil
}

// applyExecution применяет одно событие и сохраняет результат в той же транзакции, что и курсор журнала.
func applyExecution(ctx context.Context, tx pgx.Tx, x D.Execution, ev D.AgentEvent) error {
	if err := x.Apply(ev); err != nil {
		return err
	}
	raw, err := encodeExecution(x)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE runtime_executions SET data=$3 WHERE session_id=$1 AND id=$2", x.SessionID, x.ID, raw); err != nil {
		return err
	}
	if x.Projection == "pending" {
		_, err = tx.Exec(ctx, "INSERT INTO runtime_projections(session_id,execution_id,version) VALUES($1,$2,$3) ON CONFLICT(session_id,execution_id) DO UPDATE SET version=EXCLUDED.version,next_attempt=now()", x.SessionID, x.ID, x.Version)
	}
	return err
}

// updateDisplays обходит только связанные display_id текущего поколения страницами по сто ID.
func updateDisplays(ctx context.Context, tx pgx.Tx, ev D.AgentEvent) error {
	key, err := displayKey(ev.Content)
	if err != nil {
		return D.ErrProtocol
	}
	if key == "" {
		return nil
	}
	var after int64
	for {
		rows, err := tx.Query(ctx, `SELECT id,admission_order FROM runtime_executions
 WHERE session_id=$1 AND data->>'Generation'=$2 AND data->'DisplayIDs' ? $3
 AND admission_order>$4 AND id!=$5 AND COALESCE(data->>'Kind','')!='inspect'
 ORDER BY admission_order LIMIT 100`, ev.SessionID, ev.Generation, key, after, ev.ExecutionID)
		if err != nil {
			return err
		}
		ids := make([]string, 0, 100)
		for rows.Next() {
			var id string
			if err = rows.Scan(&id, &after); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			x, err := readExecution(tx.QueryRow(ctx, "SELECT data FROM runtime_executions WHERE session_id=$1 AND id=$2 FOR UPDATE", ev.SessionID, id))
			if err != nil {
				return err
			}
			if err = applyExecution(ctx, tx, x, ev); err != nil {
				return err
			}
		}
		if len(ids) < 100 {
			return nil
		}
	}
}

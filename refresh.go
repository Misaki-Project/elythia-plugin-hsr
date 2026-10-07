package hsr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/elythia-network/elythia/plugin"
)

func refreshInterval(c context.Context, api plugin.API, userID string) (time.Duration, error) {
	if api == nil {
		return 0, plugin.Errorf(503, "スターレイルの取得間隔を確認できません")
	}
	raw, err := api.AsUser(userID).Call(c, "i", map[string]any{})
	if err != nil {
		return 0, err
	}
	var me struct {
		Host     *string                    `json:"host"`
		Policies map[string]json.RawMessage `json:"policies"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		return 0, err
	}
	if me.Host != nil {
		return 0, plugin.Errorf(403, "ローカルアカウントが必要です")
	}
	value, ok := me.Policies["hsrRefreshIntervalMinutes"]
	if !ok {
		return 10 * time.Minute, nil
	}
	var minutes float64
	if json.Unmarshal(value, &minutes) != nil || minutes < 1 || minutes > 1440 || math.Trunc(minutes) != minutes {
		return 0, plugin.Errorf(503, "取得間隔は1〜1440分の整数で指定してください")
	}
	return time.Duration(minutes) * time.Minute, nil
}

func refreshDue(now time.Time, fetched, expires, attempted sql.NullTime, interval time.Duration) bool {
	if expires.Valid && now.Before(expires.Time) {
		return false
	}
	if attempted.Valid && (!fetched.Valid || attempted.Time.After(fetched.Time)) {
		fetched = attempted
	}
	return !fetched.Valid || !now.Before(fetched.Time.Add(interval))
}

func refreshUID(c context.Context, api plugin.API, db *sql.DB, client *enkaClient, uid string) (bool, error) {
	tx, err := db.BeginTx(c, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(c, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, uid, linkUIDLock); err != nil {
		return false, err
	}
	var owner string
	var fetched, expires, attempted sql.NullTime
	var now time.Time
	err = tx.QueryRowContext(c, `SELECT a.user_id,s.fetched_at,s.expires_at,a.last_refresh_attempt_at,clock_timestamp()
		FROM accounts a LEFT JOIN snapshots s ON s.uid=a.uid WHERE a.uid=$1 FOR UPDATE OF a`, uid).Scan(&owner, &fetched, &expires, &attempted, &now)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	interval, err := refreshInterval(c, api, owner)
	if err != nil || !refreshDue(now, fetched, expires, attempted, interval) {
		return false, err
	}
	if _, err := tx.ExecContext(c, `UPDATE accounts SET last_refresh_attempt_at=clock_timestamp() WHERE uid=$1`, uid); err != nil {
		return false, err
	}
	snap, fetchErr := client.fetch(c, uid)
	if fetchErr == nil {
		if _, err := tx.ExecContext(c, `SAVEPOINT snapshot_save`); err != nil {
			return true, err
		}
		if err := saveSnapshot(c, tx, snap); err != nil {
			if _, rollbackErr := tx.ExecContext(c, `ROLLBACK TO SAVEPOINT snapshot_save`); rollbackErr != nil {
				return true, rollbackErr
			}
			fetchErr = err
		}
	}
	if err := tx.Commit(); err != nil {
		return true, err
	}
	return true, fetchErr
}

package hsr

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/elythia-network/elythia/plugin"
)

const linkCodeSymbols = "-+!?@#%&=_"
const maxVerificationAttempts = 10
const linkUserLock = 58127
const linkUIDLock = 58128

// 既存登録は所有確認の証拠ではない。削除せず隔離し、UIDを占有させない。
var securityMigration = plugin.Migration{Version: 4, SQL: `
	ALTER TABLE accounts RENAME TO accounts_unverified;
	CREATE TABLE accounts (
		user_id text NOT NULL,
		uid text NOT NULL UNIQUE,
		public_id text NOT NULL DEFAULT gen_random_uuid()::text UNIQUE,
		updated_at timestamptz NOT NULL DEFAULT now(),
		last_refresh_attempt_at timestamptz,
		CONSTRAINT verified_accounts_pkey PRIMARY KEY (user_id, uid)
	);
	CREATE TABLE link_challenges (
		user_id text PRIMARY KEY, uid text NOT NULL, code text NOT NULL,
		expires_at timestamptz NOT NULL, issued_at timestamptz NOT NULL DEFAULT now(),
		next_check_at timestamptz NOT NULL DEFAULT now(), attempts int NOT NULL DEFAULT 0
	);
	CREATE TABLE verification_cache (uid text PRIMARY KEY, signature text NOT NULL, expires_at timestamptz NOT NULL);
	CREATE TABLE user_preferences (
		user_id text PRIMARY KEY, publish_uid boolean NOT NULL DEFAULT false,
		publish_signature boolean NOT NULL DEFAULT true, ranking_enabled boolean NOT NULL DEFAULT true
	);
	DELETE FROM peer_cache;
	DELETE FROM peer_cache_pending;
	DELETE FROM peer_cache_ask;
`}

type challenge struct {
	UID         string    `json:"uid"`
	Code        string    `json:"code"`
	ExpiresAt   time.Time `json:"expiresAt"`
	NextCheckAt time.Time `json:"nextCheckAt"`
	Attempts    int       `json:"attempts"`
}

type preferences struct {
	PublishUID       bool `json:"publishUid"`
	PublishSignature bool `json:"publishSignature"`
	RankingEnabled   bool `json:"rankingEnabled"`
}

func newLinkCode() (string, error) {
	const alphabet = "0123456789" + linkCodeSymbols
	for {
		var code [6]byte
		hasSymbol := false
		for i := range code {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			code[i] = alphabet[n.Int64()]
			hasSymbol = hasSymbol || n.Int64() >= 10
		}
		if hasSymbol {
			return string(code[:]), nil
		}
	}
}

func signatureHasCode(signature, code string) bool {
	return code != "" && strings.Contains(signature, code)
}

func linkLimit(c context.Context, api plugin.API, userID string) (int, error) {
	if api == nil {
		return 0, plugin.Errorf(503, "UID連携権限を確認できません")
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
		return 0, plugin.Errorf(403, "ローカルアカウントでログインしてください")
	}
	value, ok := me.Policies["hsrUidLimit"]
	if !ok {
		return 1, nil
	}
	var limit float64
	if json.Unmarshal(value, &limit) != nil || limit < 0 || limit > 100 || math.Trunc(limit) != limit {
		return 0, plugin.Errorf(503, "UID連携上限の設定が不正です")
	}
	return int(limit), nil
}

func lockLinkUser(c context.Context, db *sql.DB, userID string) (*sql.Tx, error) {
	tx, err := db.BeginTx(c, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(c, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, userID, linkUserLock); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func checkLinkCapacity(c context.Context, tx *sql.Tx, userID, uid string, limit int) error {
	var owner string
	err := tx.QueryRowContext(c, `SELECT user_id FROM accounts WHERE uid=$1`, uid).Scan(&owner)
	if err == nil {
		return plugin.Errorf(409, "このUIDは既に連携されています")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	if err := tx.QueryRowContext(c, `SELECT count(*) FROM accounts WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return err
	}
	if count >= limit {
		return plugin.Errorf(403, "UID連携数の上限に達しています")
	}
	return nil
}

func accountStatus(req plugin.Request, ctx plugin.Context, db *sql.DB) (any, error) {
	me := req.UserID()
	if me == "" {
		return nil, plugin.Errorf(401, "ログインが必要です")
	}
	limit, err := linkLimit(req.Context(), ctx.API(), me)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(req.Context(), `SELECT uid FROM accounts WHERE user_id=$1 ORDER BY updated_at,uid`, me)
	if err != nil {
		return nil, err
	}
	uids := []string{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		uids = append(uids, uid)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	var pending challenge
	err = db.QueryRowContext(req.Context(), `SELECT uid,code,expires_at,next_check_at,attempts FROM link_challenges WHERE user_id=$1 AND expires_at>clock_timestamp()`, me).
		Scan(&pending.UID, &pending.Code, &pending.ExpiresAt, &pending.NextCheckAt, &pending.Attempts)
	var value any
	if err == nil {
		value = pending
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var legacy sql.NullString
	if err := db.QueryRowContext(req.Context(), `SELECT uid FROM accounts_unverified WHERE user_id=$1`, me).Scan(&legacy); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return map[string]any{"uids": uids, "limit": limit, "pending": value, "unverifiedUid": legacy.String}, nil
}

func loadPreferences(c context.Context, db *sql.DB, userID string) (preferences, error) {
	p := preferences{false, true, true}
	err := db.QueryRowContext(c, `SELECT publish_uid,publish_signature,ranking_enabled FROM user_preferences WHERE user_id=$1`, userID).Scan(&p.PublishUID, &p.PublishSignature, &p.RankingEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return p, err
}

// プラグインDBの存在だけで本体のユーザー可視性・削除・凍結判定を迂回しない。
func visibleUser(c context.Context, api plugin.API, viewerID, userID string) (bool, error) {
	if api == nil {
		return false, plugin.Errorf(503, "ユーザーの閲覧権限を確認できません")
	}
	caller := api.Anonymous()
	if viewerID != "" {
		caller = api.AsUser(viewerID)
	}
	raw, err := caller.Call(c, "users/show", map[string]any{"userId": userID})
	if err != nil {
		var ae *plugin.APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			return false, nil
		}
		return false, err
	}
	var user struct {
		ID          string `json:"id"`
		IsSuspended bool   `json:"isSuspended"`
		IsDeleted   bool   `json:"isDeleted"`
	}
	if err := json.Unmarshal(raw, &user); err != nil {
		return false, err
	}
	return user.ID == userID && !user.IsSuspended && !user.IsDeleted, nil
}

func registerSecurityRoutes(ctx plugin.Context, r plugin.Router, db *sql.DB, client *enkaClient) {
	r.POST("/me", func(req plugin.Request) (any, error) { return accountStatus(req, ctx, db) })
	r.POST("/me/set", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		return nil, plugin.Errorf(400, "UID連携には紐づけコードによる本人確認が必要です")
	})
	r.POST("/me/begin", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		var body struct {
			UID string `json:"uid"`
		}
		if req.Bind(&body) != nil || !uidPattern.MatchString(body.UID) {
			return nil, plugin.Errorf(400, "UIDの形式が正しくありません")
		}
		tx, err := lockLinkUser(req.Context(), db, req.UserID())
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		limit, err := linkLimit(req.Context(), ctx.API(), req.UserID())
		if err != nil {
			return nil, err
		}
		if err := checkLinkCapacity(req.Context(), tx, req.UserID(), body.UID, limit); err != nil {
			return nil, err
		}
		var recent bool
		if err := tx.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM link_challenges WHERE user_id=$1 AND issued_at>clock_timestamp()-interval '30 seconds')`, req.UserID()).Scan(&recent); err != nil {
			return nil, err
		}
		if recent {
			return nil, plugin.Errorf(429, "コードの再発行は30秒待ってから行ってください")
		}
		code, err := newLinkCode()
		if err != nil {
			return nil, err
		}
		var pending challenge
		err = tx.QueryRowContext(req.Context(), `INSERT INTO link_challenges(user_id,uid,code,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '10 minutes')
			ON CONFLICT(user_id) DO UPDATE SET uid=EXCLUDED.uid,code=EXCLUDED.code,expires_at=EXCLUDED.expires_at,issued_at=clock_timestamp(),next_check_at=GREATEST(link_challenges.next_check_at,clock_timestamp()),attempts=0
			RETURNING uid,code,expires_at,next_check_at,attempts`, req.UserID(), body.UID, code).
			Scan(&pending.UID, &pending.Code, &pending.ExpiresAt, &pending.NextCheckAt, &pending.Attempts)
		if err != nil {
			return nil, err
		}
		return pending, tx.Commit()
	})
	r.POST("/me/verify", func(req plugin.Request) (any, error) { return verifyLink(req, ctx, db, client) })
	r.POST("/me/unlink", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		var body struct {
			UID string `json:"uid"`
		}
		if req.Bind(&body) != nil || !uidPattern.MatchString(body.UID) {
			return nil, plugin.Errorf(400, "UIDの形式が正しくありません")
		}
		tx, err := lockLinkUser(req.Context(), db, req.UserID())
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		for _, query := range []string{`DELETE FROM accounts WHERE user_id=$1 AND uid=$2`, `DELETE FROM accounts_unverified WHERE user_id=$1 AND uid=$2`, `DELETE FROM link_challenges WHERE user_id=$1 AND uid=$2`} {
			if _, err := tx.ExecContext(req.Context(), query, req.UserID(), body.UID); err != nil {
				return nil, err
			}
		}
		return map[string]any{"unlinked": true}, tx.Commit()
	})
	r.POST("/me/preferences", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		return loadPreferences(req.Context(), db, req.UserID())
	})
	r.POST("/me/preferences/update", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		var body struct {
			PublishUID       *bool `json:"publishUid"`
			PublishSignature *bool `json:"publishSignature"`
			RankingEnabled   *bool `json:"rankingEnabled"`
		}
		if req.Bind(&body) != nil || body.PublishUID == nil || body.PublishSignature == nil || body.RankingEnabled == nil {
			return nil, plugin.Errorf(400, "公開・ランキング参加設定をすべて指定してください")
		}
		if _, err := linkLimit(req.Context(), ctx.API(), req.UserID()); err != nil {
			return nil, err
		}
		p := preferences{*body.PublishUID, *body.PublishSignature, *body.RankingEnabled}
		_, err := db.ExecContext(req.Context(), `INSERT INTO user_preferences(user_id,publish_uid,publish_signature,ranking_enabled) VALUES($1,$2,$3,$4)
			ON CONFLICT(user_id) DO UPDATE SET publish_uid=$2,publish_signature=$3,ranking_enabled=$4`, req.UserID(), p.PublishUID, p.PublishSignature, p.RankingEnabled)
		return p, err
	})
	r.POST("/rankings", func(req plugin.Request) (any, error) { return rankingResponse(req.Context(), ctx, db, req) })
}

func verifyLink(req plugin.Request, ctx plugin.Context, db *sql.DB, client *enkaClient) (any, error) {
	me, c := req.UserID(), req.Context()
	if me == "" {
		return nil, plugin.Errorf(401, "ログインが必要です")
	}
	var body struct {
		Code string `json:"code"`
	}
	if req.Bind(&body) != nil {
		return nil, plugin.Errorf(400, "リクエストを読めません")
	}
	tx, err := lockLinkUser(c, db, me)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var p challenge
	var now time.Time
	err = tx.QueryRowContext(c, `SELECT uid,code,expires_at,next_check_at,attempts,clock_timestamp() FROM link_challenges WHERE user_id=$1 FOR UPDATE`, me).
		Scan(&p.UID, &p.Code, &p.ExpiresAt, &p.NextCheckAt, &p.Attempts, &now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, plugin.Errorf(400, "紐づけコードを発行してください")
	}
	if err != nil {
		return nil, err
	}
	if body.Code != p.Code {
		return nil, plugin.Errorf(409, "コードが再発行されています。画面を更新してください")
	}
	if !now.Before(p.ExpiresAt) {
		return nil, plugin.Errorf(410, "コードの有効期限が切れました")
	}
	if p.Attempts >= maxVerificationAttempts {
		return nil, plugin.Errorf(429, "確認回数の上限に達しました")
	}
	if now.Before(p.NextCheckAt) {
		return map[string]any{"verified": false, "nextCheckAt": p.NextCheckAt, "expiresAt": p.ExpiresAt}, nil
	}
	limit, err := linkLimit(c, ctx.API(), me)
	if err != nil {
		return nil, err
	}
	if err := checkLinkCapacity(c, tx, me, p.UID, limit); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(c, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, p.UID, linkUIDLock); err != nil {
		return nil, err
	}
	var signature string
	var until time.Time
	err = tx.QueryRowContext(c, `SELECT signature,expires_at FROM (SELECT signature,expires_at FROM verification_cache WHERE uid=$1
		UNION ALL SELECT signature,expires_at FROM snapshots WHERE uid=$1) cached WHERE expires_at>clock_timestamp() ORDER BY expires_at DESC LIMIT 1`, p.UID).Scan(&signature, &until)
	if errors.Is(err, sql.ErrNoRows) {
		var snap *snapshot
		snap, err = client.fetch(c, p.UID)
		if err == nil {
			signature, until = snap.signature, time.Now().Add(time.Duration(snap.ttl)*time.Second)
			if _, err = tx.ExecContext(c, `SAVEPOINT verification_save`); err != nil {
				return nil, err
			}
			err = saveSnapshot(c, tx, snap)
			if err == nil {
				_, err = tx.ExecContext(c, `INSERT INTO verification_cache(uid,signature,expires_at) VALUES($1,$2,$3) ON CONFLICT(uid) DO UPDATE SET signature=EXCLUDED.signature,expires_at=EXCLUDED.expires_at`, p.UID, signature, until)
			}
			if err != nil {
				if _, rollbackErr := tx.ExecContext(c, `ROLLBACK TO SAVEPOINT verification_save`); rollbackErr != nil {
					return nil, rollbackErr
				}
			}
		}
	}
	if err != nil {
		if _, updateErr := tx.ExecContext(c, `UPDATE link_challenges SET attempts=attempts+1,next_check_at=clock_timestamp()+interval '60 seconds' WHERE user_id=$1`, me); updateErr != nil {
			return nil, updateErr
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return nil, plugin.Errorf(503, "ゲーム情報を取得できませんでした。60秒待って再確認してください")
	}
	if err := tx.QueryRowContext(c, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !now.Before(p.ExpiresAt) {
		return nil, plugin.Errorf(410, "コードの有効期限が切れました")
	}
	if !signatureHasCode(signature, p.Code) {
		if err := tx.QueryRowContext(c, `UPDATE link_challenges SET attempts=attempts+1,next_check_at=GREATEST($2,clock_timestamp()+interval '60 seconds') WHERE user_id=$1 RETURNING next_check_at`, me, until).Scan(&until); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return map[string]any{"verified": false, "nextCheckAt": until, "expiresAt": p.ExpiresAt}, nil
	}
	limit, err = linkLimit(c, ctx.API(), me)
	if err != nil {
		return nil, err
	}
	if err := checkLinkCapacity(c, tx, me, p.UID, limit); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(c, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !now.Before(p.ExpiresAt) {
		return nil, plugin.Errorf(410, "コードの有効期限が切れました")
	}
	result, err := tx.ExecContext(c, `INSERT INTO accounts(user_id,uid) VALUES($1,$2) ON CONFLICT(uid) DO NOTHING`, me, p.UID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, plugin.Errorf(409, "このUIDは既に連携されています")
	}
	if _, err := tx.ExecContext(c, `DELETE FROM link_challenges WHERE user_id=$1`, me); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(c, `DELETE FROM accounts_unverified WHERE user_id=$1 AND uid=$2`, me, p.UID); err != nil {
		return nil, err
	}
	return map[string]any{"verified": true, "uid": p.UID}, tx.Commit()
}

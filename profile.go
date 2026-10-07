package hsr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

// buildProfile assembles the display payload for a local user.
//
// 未登録なら (nil, nil)。**エラーと区別する** — 「登録していない」は普通の
// 状態で、表示側はそれを見て何も描かない。
func buildProfile(c context.Context, db *sql.DB, client *enkaClient, userID string, selectedUID ...string) (map[string]any, error) {
	filterUID := ""
	if len(selectedUID) > 0 {
		filterUID = selectedUID[0]
	}
	var (
		uid, nickname, signature, region, platform string
		level, worldLevel, friendCount, headIcon   int
		achievements, bookCount, avatarCount       int
		equipmentCount, relicCount, musicCount     int
		rogueScore, memoryLevel                    int
		charactersRaw                              []byte
		fetchedAt                                  time.Time
		publicID                                   string
		publishUID, publishSignature               bool
	)
	err := db.QueryRowContext(c, `
		SELECT a.uid, s.nickname, s.signature, s.level, s.world_level, s.region,
		       s.platform, s.friend_count, s.head_icon, s.achievements, s.book_count,
		       s.avatar_count, s.equipment_count, s.relic_count, s.music_count,
		       s.rogue_score, s.memory_level, s.characters, s.fetched_at,
		       a.public_id, COALESCE(p.publish_uid,false), COALESCE(p.publish_signature,true)
		FROM accounts a JOIN snapshots s ON s.uid = a.uid
		LEFT JOIN user_preferences p ON p.user_id=a.user_id
		WHERE a.user_id = $1 AND ($2='' OR a.uid=$2)
		ORDER BY a.updated_at,a.uid LIMIT 1
	`, userID, filterUID).Scan(&uid, &nickname, &signature, &level, &worldLevel, &region,
		&platform, &friendCount, &headIcon, &achievements, &bookCount,
		&avatarCount, &equipmentCount, &relicCount, &musicCount,
		&rogueScore, &memoryLevel, &charactersRaw, &fetchedAt, &publicID, &publishUID, &publishSignature)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// 壊れた JSON でカード全体を落とさない。詳細が出ないだけで済ませる。
	characters := []character{}
	if len(charactersRaw) > 0 {
		_ = json.Unmarshal(charactersRaw, &characters)
	}

	// アイコンは**自分のプロキシ経由の URL**として返す。CSP が
	// `img-src 'self'` なので、取得元の URL を渡しても表示できない。
	icon := ""
	if headIcon != 0 {
		if p, ok := client.masters.pfps.Get(c)[strconv.Itoa(headIcon)]; ok {
			icon = assetURL(p.Icon)
		}
	}

	profile := map[string]any{
		"accountId":      publicID,
		"linked":         true,
		"uid":            uid,
		"nickname":       nickname,
		"signature":      signature,
		"level":          level,
		"worldLevel":     worldLevel,
		"region":         region,
		"platform":       platform,
		"friendCount":    friendCount,
		"achievements":   achievements,
		"bookCount":      bookCount,
		"avatarCount":    avatarCount,
		"equipmentCount": equipmentCount,
		"relicCount":     relicCount,
		"musicCount":     musicCount,
		"rogueScore":     rogueScore,
		"memoryLevel":    memoryLevel,
		"profileIcon":    icon,
		"characters":     characters,
		"fetchedAt":      fetchedAt,
	}
	if !publishUID {
		delete(profile, "uid")
	}
	if !publishSignature {
		delete(profile, "signature")
	}
	return profile, nil
}

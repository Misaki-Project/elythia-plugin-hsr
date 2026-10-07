package hsr

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/elythia-network/elythia/plugin"
)

const maxRankingCandidates = 10000

type rankingEntry struct {
	Rank      int       `json:"rank"`
	UserID    string    `json:"userId"`
	AccountID string    `json:"accountId"`
	UID       string    `json:"uid,omitempty"`
	Nickname  string    `json:"nickname"`
	Value     int       `json:"value"`
	FetchedAt time.Time `json:"fetchedAt"`
}

func rankingResponse(c context.Context, ctx plugin.Context, db *sql.DB, req plugin.Request) (any, error) {
	var body struct {
		Metric    string `json:"metric"`
		Limit     int    `json:"limit"`
		Offset    int    `json:"offset"`
		AccountID string `json:"accountId"`
	}
	if req.Bind(&body) != nil || body.Offset < 0 || body.Offset > maxRankingCandidates || body.Limit < 0 || body.Limit > 100 || len(body.AccountID) > 128 || (body.AccountID != "" && body.Offset != 0) {
		return nil, plugin.Errorf(400, "ランキングの条件が不正です")
	}
	if body.Metric != "achievements" {
		return nil, plugin.Errorf(400, "ランキングの種類が不正です")
	}
	if body.Limit == 0 {
		body.Limit = 50
	}
	if ctx.API() == nil {
		return nil, plugin.Errorf(503, "ランキングのユーザー情報を確認できません")
	}
	rows, err := db.QueryContext(c, `SELECT a.user_id,a.public_id,CASE WHEN COALESCE(p.publish_uid,false) THEN a.uid ELSE '' END,
		s.nickname,s.achievements,s.fetched_at FROM accounts a JOIN snapshots s ON s.uid=a.uid
		LEFT JOIN user_preferences p ON p.user_id=a.user_id WHERE COALESCE(p.ranking_enabled,true)
		ORDER BY s.achievements DESC,a.public_id LIMIT $1`, maxRankingCandidates+1)
	if err != nil {
		return nil, err
	}
	entries := []rankingEntry{}
	for rows.Next() {
		var e rankingEntry
		if err := rows.Scan(&e.UserID, &e.AccountID, &e.UID, &e.Nickname, &e.Value, &e.FetchedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if len(entries) > maxRankingCandidates {
		return nil, plugin.Errorf(503, "ランキングの集計対象が上限を超えています")
	}
	ids, seenIDs := []string{}, map[string]bool{}
	for _, e := range entries {
		if !seenIDs[e.UserID] {
			ids = append(ids, e.UserID)
			seenIDs[e.UserID] = true
		}
	}
	eligible := map[string]bool{}
	for start := 0; start < len(ids); start += 100 {
		raw, err := ctx.API().Anonymous().Call(c, "users/show", map[string]any{"userIds": ids[start:min(start+100, len(ids))]})
		if err != nil {
			return nil, err
		}
		var users []struct {
			ID          string  `json:"id"`
			Host        *string `json:"host"`
			IsSuspended bool    `json:"isSuspended"`
			IsDeleted   bool    `json:"isDeleted"`
		}
		if err := json.Unmarshal(raw, &users); err != nil {
			return nil, err
		}
		for _, user := range users {
			if seenIDs[user.ID] && user.Host == nil && !user.IsSuspended && !user.IsDeleted {
				eligible[user.ID] = true
			}
		}
	}
	visible := []rankingEntry{}
	seen, rank, previous := 0, 0, -1
	hasMore := false
	for _, e := range entries {
		if !eligible[e.UserID] {
			continue
		}
		seen++
		if seen == 1 || e.Value != previous {
			rank = seen
		}
		previous, e.Rank = e.Value, rank
		if body.AccountID != "" && e.AccountID != body.AccountID {
			continue
		}
		if seen <= body.Offset {
			continue
		}
		if len(visible) == body.Limit {
			hasMore = true
			break
		}
		visible = append(visible, e)
	}
	return map[string]any{"metric": body.Metric, "entries": visible, "limit": body.Limit, "offset": body.Offset, "hasMore": hasMore}, nil
}

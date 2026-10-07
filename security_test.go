package hsr

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elythia-network/elythia/plugin"
	"github.com/elythia-network/elythia/plugin/plugintest"
)

func TestLinkCode(t *testing.T) {
	for i := 0; i < 1000; i++ {
		code, err := newLinkCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 6 || !strings.ContainsAny(code, linkCodeSymbols) {
			t.Fatalf("不正なコード: %q", code)
		}
		if !signatureHasCode("prefix "+code+" suffix", code) || signatureHasCode(code[:5], code) || signatureHasCode("", "") {
			t.Fatal("部分コードを認証している")
		}
	}
}

func securityHarness(t *testing.T, api plugin.API) (*sql.DB, plugintest.Handlers, *atomic.Int32, *string) {
	t.Helper()
	db := testDB(t)
	count := &atomic.Int32{}
	signature := new(string)
	payload := testdata(t, "uid")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		body["detailInfo"].(map[string]any)["signature"] = *signature
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	h := plugintest.New(t).WithName("hsr").WithDB(db).WithAPI(api).WithConfig(testConfig(t, srv.URL)).Routes(Plugin)
	return db, h, count, signature
}

func challengeRequest(t *testing.T, h plugintest.Handlers, user, uid string) challenge {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"uid": uid})
	res, err := h.Call(t, "POST /me/begin", plugintest.Request{UserID: user, Body: string(body)})
	if err != nil {
		t.Fatal(err)
	}
	return res.(challenge)
}

func verifyRequest(t *testing.T, h plugintest.Handlers, user, code string) (any, error) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code})
	return h.Call(t, "POST /me/verify", plugintest.Request{UserID: user, Body: string(body)})
}

func TestVerificationOwnershipPrivacyAndUnlink(t *testing.T) {
	db, h, count, signature := securityHarness(t, &fakeAPI{resp: json.RawMessage(`{"id":"u1","host":null,"policies":{"hsrUidLimit":2}}`)})
	p := challengeRequest(t, h, "u1", "800000000")
	*signature = "before " + p.Code + " after"
	res, err := verifyRequest(t, h, "u1", p.Code)
	if err != nil || !res.(map[string]any)["verified"].(bool) {
		t.Fatalf("認証失敗: %v %v", res, err)
	}
	if count.Load() != 1 {
		t.Fatal("余計な取得")
	}
	profile, err := h.Call(t, "POST /profile", plugintest.Request{Body: `{"userId":"u1"}`})
	if err != nil {
		t.Fatal(err)
	}
	public := profile.(map[string]any)
	if _, ok := public["uid"]; ok {
		t.Fatal("既定でUIDを公開している")
	}
	if public["accountId"] == "" {
		t.Fatal("公開IDがない")
	}
	if _, err := h.Call(t, "POST /me/begin", plugintest.Request{UserID: "u2", Body: `{"uid":"800000000"}`}); err == nil {
		t.Fatal("所有済みUIDを重複連携している")
	}
	_, err = h.Call(t, "POST /me/preferences/update", plugintest.Request{UserID: "u1", Body: `{"publishUid":true,"publishSignature":false,"rankingEnabled":false}`})
	if err != nil {
		t.Fatal(err)
	}
	profile, err = h.Call(t, "POST /profile", plugintest.Request{Body: `{"userId":"u1"}`})
	if err != nil {
		t.Fatal(err)
	}
	public = profile.(map[string]any)
	if public["uid"] != "800000000" {
		t.Fatal("UID公開設定が無効")
	}
	if _, ok := public["signature"]; ok {
		t.Fatal("非公開signatureが漏れている")
	}
	if _, err := h.Call(t, "POST /me/unlink", plugintest.Request{UserID: "u2", Body: `{"uid":"800000000"}`}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("他人のUIDを解除: %d %v", n, err)
	}
	if _, err := h.Call(t, "POST /me/unlink", plugintest.Request{UserID: "u1", Body: `{"uid":"800000000"}`}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("解除失敗: %d %v", n, err)
	}
}

func TestVerificationTTLWaitingExpiryAndAttemptLimit(t *testing.T) {
	db, h, count, signature := securityHarness(t, &fakeAPI{resp: json.RawMessage(`{"id":"u1","host":null,"policies":{}}`)})
	p := challengeRequest(t, h, "u1", "800000000")
	*signature = p.Code[:5]
	res, err := verifyRequest(t, h, "u1", p.Code)
	if err != nil || res.(map[string]any)["verified"] != false {
		t.Fatalf("部分一致が認証された: %v %v", res, err)
	}
	next := res.(map[string]any)["nextCheckAt"].(time.Time)
	if next.Before(time.Now().Add(59 * time.Second)) {
		t.Fatal("60秒待機がない")
	}
	*signature = p.Code
	if _, err := verifyRequest(t, h, "u1", p.Code); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("待機中に再取得")
	}
	if _, err := db.Exec(`UPDATE link_challenges SET next_check_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRequest(t, h, "u1", p.Code); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("TTL中に再取得")
	}
	if _, err := db.Exec(`UPDATE link_challenges SET attempts=10,next_check_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRequest(t, h, "u1", p.Code); err == nil {
		t.Fatal("試行上限無視")
	}
	if _, err := db.Exec(`UPDATE link_challenges SET attempts=0,expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyRequest(t, h, "u1", p.Code); err == nil {
		t.Fatal("期限切れ無視")
	}
}

func TestVerificationPolicyChangedBeforeCompletion(t *testing.T) {
	api := &fakeAPI{resp: json.RawMessage(`{"host":null,"policies":{"hsrUidLimit":1}}`)}
	db, h, count, signature := securityHarness(t, api)
	p := challengeRequest(t, h, "u1", "800000000")
	*signature = p.Code
	api.resp = json.RawMessage(`{"host":null,"policies":{"hsrUidLimit":0}}`)
	if _, err := verifyRequest(t, h, "u1", p.Code); err == nil {
		t.Fatal("連携禁止ポリシー無視")
	}
	if count.Load() != 0 {
		t.Fatal("禁止UIDを取得")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&n); err != nil || n != 0 {
		t.Fatal("禁止UIDを保存")
	}
}

func TestMigrationKeepsLegacyButDoesNotPublish(t *testing.T) {
	db := testDB(t)
	for _, m := range append(migrations, peerCacheMigration...) {
		if _, err := db.Exec(m.SQL); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO accounts(user_id,uid) VALUES('u1','800000000')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(securityMigration.SQL); err != nil {
		t.Fatal(err)
	}
	var legacy, verified int
	if err := db.QueryRow(`SELECT count(*) FROM accounts_unverified`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&verified); err != nil {
		t.Fatal(err)
	}
	if legacy != 1 || verified != 0 {
		t.Fatal("旧登録を所有証明にしている")
	}
}

func TestRefreshDueTTLAndAttempt(t *testing.T) {
	now := time.Now()
	past := sql.NullTime{Time: now.Add(-time.Hour), Valid: true}
	future := sql.NullTime{Time: now.Add(time.Minute), Valid: true}
	recent := sql.NullTime{Time: now.Add(-time.Minute), Valid: true}
	if refreshDue(now, past, future, past, 10*time.Minute) || refreshDue(now, past, past, recent, 10*time.Minute) || !refreshDue(now, past, past, past, 10*time.Minute) {
		t.Fatal("TTL・試行間隔判定が不正")
	}
}

func TestConcurrentUIDVerificationHasOneOwner(t *testing.T) {
	db, h, count, signature := securityHarness(t, &fakeAPI{resp: json.RawMessage(`{"host":null,"policies":{}}`)})
	one := challengeRequest(t, h, "u1", "800000000")
	two := challengeRequest(t, h, "u2", "800000000")
	*signature = one.Code + " " + two.Code
	start := make(chan struct{})
	results := make(chan bool, 2)
	for _, item := range []struct{ user, code string }{{"u1", one.Code}, {"u2", two.Code}} {
		go func(user, code string) {
			<-start
			res, err := verifyRequest(t, h, user, code)
			results <- err == nil && res.(map[string]any)["verified"] == true
		}(item.user, item.code)
	}
	close(start)
	first, second := <-results, <-results
	if first == second {
		t.Fatalf("認証成功は1人だけ: %v %v", first, second)
	}
	var owners int
	if err := db.QueryRow(`SELECT count(*) FROM accounts WHERE uid='800000000'`).Scan(&owners); err != nil || owners != 1 {
		t.Fatalf("所有者=%d err=%v", owners, err)
	}
	if count.Load() != 1 {
		t.Fatalf("TTL中に重複取得: %d", count.Load())
	}
}

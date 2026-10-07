package hsr

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/elythia-network/elythia/plugin"
	"github.com/elythia-network/elythia/plugin/plugintest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

const testSchema = "plugin_hsr_test"

// testDB opens a throwaway schema for one test.
//
// フェイクの DB は使わない。SQL の挙動を模した偽物は本物とずれ、通ったのに
// 本番で落ちる形のテストになる。
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	base := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envOr("TEST_DB_HOST", "localhost"), envOr("TEST_DB_PORT", "5432"),
		envOr("TEST_DB_USER", "mk"), envOr("TEST_DB_PASS", "mk"),
		envOr("TEST_DB_NAME", "misskey_test"))

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, q := range []string{
		`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`,
		`CREATE SCHEMA ` + testSchema,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	db, err := sql.Open("pgx", base+" search_path="+testSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		if a, err := sql.Open("pgx", base); err == nil {
			_, _ = a.Exec(`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`)
			_ = a.Close()
		}
	})
	return db
}

// testConfig points both the API and the master data at local test servers.
//
// **マスターの取得先も差し替える。** 既定は GitHub なので、指定しないと
// テストが外に出て遅くなる。
func testConfig(t *testing.T, enkaURL string) map[string]any {
	t.Helper()
	return map[string]any{
		"endpoint":       enkaURL,
		"userAgent":      "test/1.0",
		"timeoutSeconds": 5,
		"language":       "ja",
		"masterEndpoint": masterServer(t).URL + "/",
	}
}

// setupRoutes wires the plugin against a throwaway schema and a fake Enka.
func setupRoutes(t *testing.T, enkaURL string, verified ...bool) plugintest.Handlers {
	t.Helper()
	db := testDB(t)
	h := plugintest.New(t).
		WithName("hsr").
		WithDB(db).
		WithAPI(&fakeAPI{resp: json.RawMessage(`{"id":"u1","host":null,"policies":{}}`)}).
		WithConfig(testConfig(t, enkaURL)).
		Routes(Plugin)
	if len(verified) > 0 && verified[0] {
		seedVerifiedProfile(t, db, enkaURL)
	}
	return h
}

// 表示・jobのfixture。本人確認の挙動はsecurity_test.goの実APIフローで検査する。
func seedVerifiedProfile(t *testing.T, db *sql.DB, enkaURL string) {
	t.Helper()
	client := newEnkaClient(settings{Endpoint: enkaURL, UserAgent: "test", TimeoutSeconds: 5, Language: "ja", MasterEndpoint: masterServer(t).URL + "/"})
	snap, err := client.fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSnapshot(context.Background(), db, snap); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(user_id,uid) VALUES('u1','800000000')`); err != nil {
		t.Fatal(err)
	}
}

func TestRoutes_SetAndShowProfile(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	h := setupRoutes(t, srv.URL, true)
	if _, err := h.Call(t, "POST /me/set", plugintest.Request{UserID: "u1", Body: `{"uid":"800000000"}`}); err == nil {
		t.Fatal("直接登録を許可している")
	}

	res, err := h.Call(t, "POST /profile", plugintest.Request{Body: `{"userId":"u1"}`})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["linked"] != true || m["nickname"] != "開拓者" || m["level"] != 70 {
		t.Fatalf("想定と違う: %+v", m)
	}
	if m["achievements"] != 864 {
		t.Errorf("戦績が保存されていない: %+v", m["achievements"])
	}
	// アイコンは自分のプロキシ経由で返す (CSP が img-src 'self')。
	if icon, _ := m["profileIcon"].(string); !strings.HasPrefix(icon, assetRoutePrefix) {
		t.Errorf("アイコンが proxy 経由でない: %q", icon)
	}
	chars, _ := m["characters"].([]character)
	if len(chars) != 1 || chars[0].Name != "キュレネ" {
		t.Errorf("キャラが保存されていない: %+v", m["characters"])
	}
}

// 未登録は「無い」であってエラーではない。表示側はこれを見て何も描かない。
func TestRoutes_ProfileOfUnlinkedUser(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	h := setupRoutes(t, srv.URL)

	res, err := h.Call(t, "POST /profile", plugintest.Request{Body: `{"userId":"nobody"}`})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["linked"] != false {
		t.Fatalf("linked=false であるべき: %+v", res)
	}
}

func TestRoutes_ProfileRequiresUserID(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	h := setupRoutes(t, srv.URL)

	if _, err := h.Call(t, "POST /profile", plugintest.Request{Body: `{}`}); err == nil {
		t.Fatal("userId 無しを通している")
	}
}

func TestRoutes_RequiresLogin(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, []byte(`{}`))
	h := setupRoutes(t, srv.URL)

	for _, key := range []string{"POST /me", "POST /me/set"} {
		if _, err := h.Call(t, key, plugintest.Request{Body: `{"uid":"800000000"}`}); err == nil {
			t.Fatalf("%s: 未ログインを弾いていない", key)
		}
	}
}

func TestRoutes_MeWithoutRegistration(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, []byte(`{}`))
	h := setupRoutes(t, srv.URL)

	res, err := h.Call(t, "POST /me", plugintest.Request{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.(map[string]any)["uids"].([]string)) != 0 {
		t.Fatalf("未登録なのに UID がある: %+v", res)
	}
}

// **存在しない UID を黙って保存しない。** プロフィールに何も出ない理由が
// 利用者に分からなくなる。
func TestRoutes_RejectsUnknownUID(t *testing.T) {
	srv := fakeEnka(t, http.StatusNotFound, []byte(`{}`))
	h := setupRoutes(t, srv.URL)

	_, err := h.Call(t, "POST /me/set", plugintest.Request{UserID: "u1", Body: `{"uid":"800000000"}`})
	if err == nil {
		t.Fatal("エラーにならない")
	}
	if !strings.Contains(err.Error(), "本人確認") {
		t.Fatalf("理由が伝わらない: %v", err)
	}
}

// 取得失敗で所有確認を迂回してはいけない。
func TestRoutes_DoesNotLinkDuringUpstreamOutage(t *testing.T) {
	srv := fakeEnka(t, http.StatusFailedDependency, []byte(`{"message":"game servers down"}`))
	h := setupRoutes(t, srv.URL)

	begin, err := h.Call(t, "POST /me/begin", plugintest.Request{UserID: "u1", Body: `{"uid":"800000000"}`})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": begin.(challenge).Code})
	if _, err := h.Call(t, "POST /me/verify", plugintest.Request{UserID: "u1", Body: string(body)}); err == nil {
		t.Fatal("上流障害で認証に成功している")
	}

	res, err := h.Call(t, "POST /me", plugintest.Request{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.(map[string]any)["uids"].([]string)) != 0 {
		t.Fatalf("未確認UIDを保存している: %+v", res)
	}
}

// 指定した本人のUIDだけを解除する。
func TestRoutes_UnlinkVerifiedUID(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	h := setupRoutes(t, srv.URL, true)
	if _, err := h.Call(t, "POST /me/unlink", plugintest.Request{UserID: "u1", Body: `{"uid":"800000000"}`}); err != nil {
		t.Fatal(err)
	}

	res, _ := h.Call(t, "POST /me", plugintest.Request{UserID: "u1"})
	if len(res.(map[string]any)["uids"].([]string)) != 0 {
		t.Fatalf("解除されていない: %+v", res)
	}
}

func TestRoutes_RejectsBadUIDFormat(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, []byte(`{}`))
	h := setupRoutes(t, srv.URL)

	for _, body := range []string{`{"uid":"abc"}`, `{"uid":"1"}`, `not json`} {
		if _, err := h.Call(t, "POST /me/begin", plugintest.Request{UserID: "u1", Body: body}); err == nil {
			t.Errorf("不正な入力を通した: %s", body)
		}
	}
}

// --- 画像プロキシ ---

func TestRoutes_Asset(t *testing.T) {
	var served bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui/hsr/SpriteOutput/AvatarRoundIcon/1415.png" {
			served = true
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes(t, 32, 32))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	h := setupRoutes(t, srv.URL)

	res, err := h.Call(t, "GET /asset/*", plugintest.Request{
		Params: map[string]string{"*": "SpriteOutput/AvatarRoundIcon/1415.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, ok := res.(plugin.Blob)
	if !ok {
		t.Fatalf("Blob で返していない: %T", res)
	}
	if blob.ContentType != "image/png" || len(blob.Body) == 0 {
		t.Errorf("応答が空: %+v", blob.ContentType)
	}
	if blob.CacheControl == "" {
		t.Error("キャッシュさせていない (取得元に優しくない)")
	}
	if !served {
		t.Error("取得元に行っていない")
	}

	// **パスの検証はルートでも効くこと。**
	if _, err := h.Call(t, "GET /asset/*", plugintest.Request{
		Params: map[string]string{"*": "../../etc/passwd.png"},
	}); err == nil {
		t.Error("不正なパスを通した")
	}
	if _, err := h.Call(t, "GET /asset/*", plugintest.Request{
		Params: map[string]string{"*": "SpriteOutput/Missing.png"},
	}); err == nil {
		t.Error("存在しない asset をエラーにしていない")
	}
}

// --- ジョブ ---

// 期限切れのものだけを取り直すこと。上流が落ちていても古いデータは消さない。
func TestJobs_RefreshKeepsStaleOnFailure(t *testing.T) {
	db := testDB(t)

	ok := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	_ = plugintest.New(t).WithName("hsr").WithDB(db).
		WithConfig(testConfig(t, ok.URL)).Routes(Plugin)
	seedVerifiedProfile(t, db, ok.URL)

	// 期限切れにしてから、上流が落ちている状態で更新を走らせる。
	if _, err := db.Exec(`UPDATE snapshots SET expires_at = now() - interval '1 hour', fetched_at=now()-interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	down := fakeEnka(t, http.StatusFailedDependency, []byte(`{"message":"down"}`))
	jobs := plugintest.New(t).WithName("hsr").WithDB(db).
		WithAPI(&fakeAPI{resp: json.RawMessage(`{"host":null,"policies":{}}`)}).
		WithConfig(testConfig(t, down.URL)).Jobs(Plugin)

	if err := jobs.Run(t, "refresh", ""); err != nil {
		t.Fatalf("1 件の失敗で全体を止めない: %v", err)
	}

	var nickname string
	if err := db.QueryRow(`SELECT nickname FROM snapshots WHERE uid = '800000000'`).Scan(&nickname); err != nil {
		t.Fatal(err)
	}
	if nickname != "開拓者" {
		t.Fatalf("上流が落ちていても古いデータを保持する: %q", nickname)
	}
}

// 期限が切れていれば取り直すこと。
func TestJobs_RefreshUpdatesExpired(t *testing.T) {
	db := testDB(t)
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	harness := plugintest.New(t).WithName("hsr").WithDB(db).WithConfig(testConfig(t, srv.URL))

	_ = harness.Routes(Plugin)
	seedVerifiedProfile(t, db, srv.URL)
	if _, err := db.Exec(`UPDATE snapshots SET nickname = 'stale', expires_at = now() - interval '1 hour', fetched_at=now()-interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}

	if err := plugintest.New(t).WithName("hsr").WithDB(db).
		WithAPI(&fakeAPI{resp: json.RawMessage(`{"host":null,"policies":{}}`)}).
		WithConfig(testConfig(t, srv.URL)).Jobs(Plugin).Run(t, "refresh", ""); err != nil {
		t.Fatal(err)
	}

	var nickname string
	if err := db.QueryRow(`SELECT nickname FROM snapshots WHERE uid = '800000000'`).Scan(&nickname); err != nil {
		t.Fatal(err)
	}
	if nickname != "開拓者" {
		t.Fatalf("取り直していない: %q", nickname)
	}
}

// cron が登録されていること。
func TestJobs_RegistersSchedule(t *testing.T) {
	jobs := plugintest.New(t).WithName("hsr").WithDB(testDB(t)).Jobs(Plugin)

	if len(jobs.Schedules) != 1 || jobs.Schedules[0].Name != "refresh" {
		t.Fatalf("想定と違う: %+v", jobs.Schedules)
	}
}

package hsr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/elythia-network/elythia/plugin/plugintest"
)

func TestRankingPrivacyEligibilityTiesAndPagination(t *testing.T) {
	db := testDB(t)
	api := &fakeAPI{resp: json.RawMessage(`[{"id":"u1","host":null},{"id":"u2","host":null},{"id":"u3","isSuspended":true},{"id":"u4","isDeleted":true},{"id":"u5","host":"remote.example"},{"id":"u6","host":null}]`)}
	srv := fakeEnka(t, http.StatusOK, testdata(t, "uid"))
	h := plugintest.New(t).WithName("hsr").WithDB(db).WithAPI(api).WithConfig(testConfig(t, srv.URL)).Routes(Plugin)
	for i := 1; i <= 6; i++ {
		uid := fmt.Sprintf("80000000%d", i)
		if _, err := db.Exec(`INSERT INTO accounts(user_id,uid,public_id) VALUES($1,$2,$3)`, fmt.Sprintf("u%d", i), uid, fmt.Sprintf("a%d", i)); err != nil {
			t.Fatal(err)
		}
		if err := saveSnapshot(context.Background(), db, &snapshot{uid: uid, nickname: fmt.Sprintf("player%d", i), achievements: 100, ttl: 60}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO user_preferences(user_id,publish_uid,ranking_enabled) VALUES('u2',true,true),('u6',true,false)`); err != nil {
		t.Fatal(err)
	}
	res, err := h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"achievements","limit":1}`})
	if err != nil {
		t.Fatal(err)
	}
	page := res.(map[string]any)
	entries := page["entries"].([]rankingEntry)
	if len(entries) != 1 || entries[0].UserID != "u1" || entries[0].UID != "" || entries[0].Rank != 1 || page["hasMore"] != true {
		t.Fatalf("最初のページが不正: %+v", page)
	}
	res, err = h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"achievements","offset":1,"limit":1}`})
	if err != nil {
		t.Fatal(err)
	}
	page = res.(map[string]any)
	entries = page["entries"].([]rankingEntry)
	if len(entries) != 1 || entries[0].UserID != "u2" || entries[0].UID != "800000002" || entries[0].Rank != 1 || page["hasMore"] != false {
		t.Fatalf("次のページが不正: %+v", page)
	}
	for _, body := range []string{`{"metric":"memory"}`, `{"metric":"achievements","offset":-1}`, `{"metric":"achievements","limit":101}`} {
		if _, err := h.Call(t, "POST /rankings", plugintest.Request{Body: body}); err == nil {
			t.Fatalf("不正な条件を通過: %s", body)
		}
	}
}

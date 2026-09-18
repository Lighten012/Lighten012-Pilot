package systemlogs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestQueryValidation(t *testing.T) {
	now := time.Now()
	for _, query := range []string{"source=ssh.service", "level=debug", "window=all", "limit=10000", "cursor=--file=/etc/shadow", "until=1"} {
		if _, e := Parse(mustValues(query), now); e == nil {
			t.Fatal(query)
		}
	}
	q, e := Parse(mustValues("source=network&level=warning&search=%3B+reboot"), now)
	if e != nil {
		t.Fatal(e)
	}
	a := strings.Join(Args(q), " ")
	if strings.Contains(a, "reboot") || !strings.Contains(a, "pilot-netd.service") || !strings.Contains(a, "0..4") {
		t.Fatal(a)
	}
}
func mustValues(s string) url.Values { v, _ := url.ParseQuery(s); return v }
func record(cursor, message string) string {
	b, _ := json.Marshal(map[string]string{"__CURSOR": cursor, "MESSAGE": message, "PRIORITY": "4", "__REALTIME_TIMESTAMP": "1000000", "_SYSTEMD_UNIT": "pilot-netd.service"})
	return string(b) + "\n"
}
func TestPagination(t *testing.T) {
	data := record("c3", "hello") + record("c2", "keep") + record("c1", "keep")
	q := Query{Limit: 1, Search: "keep", Until: 1000}
	r, e := Decode([]byte(data), q)
	if e != nil || len(r.Entries) != 1 || r.Entries[0].Cursor != "c2" || r.NextCursor != "c2" || !r.HasMore {
		t.Fatal(r, e)
	}
	q.Cursor = "c2"
	r, e = Decode([]byte(record("c2", "keep")+record("c1", "keep")), q)
	if e != nil || len(r.Entries) != 1 || r.Entries[0].Cursor != "c1" || r.HasMore {
		t.Fatal(r, e)
	}
	var many strings.Builder
	for i := 1001; i > 0; i-- {
		many.WriteString(record(fmt.Sprintf("c%d", i), "none"))
	}
	q.Cursor = ""
	r, e = Decode([]byte(many.String()), q)
	if e != nil || len(r.Entries) != 0 || r.NextCursor != "c2" || !r.HasMore {
		t.Fatal(r, e)
	}
}
func TestDecodeAndBounds(t *testing.T) {
	raw := `{"__CURSOR":"c","MESSAGE":[104,105],"PRIORITY":"3","__REALTIME_TIMESTAMP":"1000000"}`
	r, e := Decode([]byte(raw), Query{Limit: 100})
	if e != nil || r.Entries[0].Message != "hi" || r.Entries[0].Time != 1000 {
		t.Fatal(r, e)
	}
	r, e = Decode([]byte(record("c", strings.Repeat("测", 3000))), Query{Limit: 100})
	if e != nil || !strings.Contains(r.Entries[0].Message, "已截断") {
		t.Fatal(e)
	}
	b := &bounded{limit: 3}
	if _, e = b.Write([]byte("1234")); e == nil {
		t.Fatal("unbounded")
	}
	if _, e = io.Copy(b, io.LimitReader(strings.NewReader("1234"), 4)); e == nil {
		t.Fatal("copy bypassed bound")
	}
}
func TestHandler(t *testing.T) {
	h := Handler(func(_ context.Context, q Query) (Result, error) {
		return Result{Entries: []Entry{}, Until: q.Until}, nil
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/logs?source=web", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/logs?source=anything", nil))
	if w.Code != 400 {
		t.Fatal(w)
	}
}

package systemlogs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Query struct {
	Source string
	Level  string
	Window string
	Search string
	Cursor string
	Until  int64
	Limit  int
}
type Entry struct {
	Cursor   string `json:"cursor"`
	Time     int64  `json:"time"`
	Priority int    `json:"priority"`
	Unit     string `json:"unit"`
	Message  string `json:"message"`
}
type Result struct {
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
	Until      int64   `json:"until"`
	Scanned    int     `json:"scanned"`
}

var cursorPattern = regexp.MustCompile(`^[a-zA-Z0-9_=;.-]+$`)

func Parse(v url.Values, now time.Time) (Query, error) {
	q := Query{Source: v.Get("source"), Level: v.Get("level"), Window: v.Get("window"), Search: v.Get("search"), Cursor: v.Get("cursor"), Until: now.UnixMilli(), Limit: 100}
	if q.Source == "" {
		q.Source = "pilot"
	}
	if q.Level == "" {
		q.Level = "all"
	}
	if q.Window == "" {
		q.Window = "24h"
	}
	if q.Source != "pilot" && q.Source != "web" && q.Source != "network" && q.Source != "system" && q.Source != "kernel" {
		return q, fmt.Errorf("日志来源无效")
	}
	if q.Level != "all" && q.Level != "error" && q.Level != "warning" {
		return q, fmt.Errorf("日志级别无效")
	}
	if q.Window != "1h" && q.Window != "24h" && q.Window != "7d" {
		return q, fmt.Errorf("时间范围无效")
	}
	if len(q.Search) > 256 || !utf8.ValidString(q.Search) || strings.ContainsRune(q.Search, 0) {
		return q, fmt.Errorf("关键词过长或无效")
	}
	if len(q.Cursor) > 2048 || q.Cursor != "" && !cursorPattern.MatchString(q.Cursor) {
		return q, fmt.Errorf("分页游标无效，请刷新")
	}
	if s := v.Get("limit"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 200 {
			return q, fmt.Errorf("每页条数须为 1–200")
		}
		q.Limit = n
	}
	if s := v.Get("until"); s != "" {
		n, e := strconv.ParseInt(s, 10, 64)
		if e != nil || n <= 0 || n > now.Add(time.Minute).UnixMilli() || n < now.Add(-8*24*time.Hour).UnixMilli() {
			return q, fmt.Errorf("分页时间已过期，请刷新")
		}
		q.Until = n
	}
	return q, nil
}
func windowDuration(q Query) time.Duration {
	duration := 24 * time.Hour
	if q.Window == "1h" {
		duration = time.Hour
	}
	if q.Window == "7d" {
		duration = 7 * 24 * time.Hour
	}
	return duration
}
func Args(q Query) []string {
	args := []string{"--no-pager", "--quiet", "--output=json", "--all", "--reverse", "--lines=1001", "--output-fields=__CURSOR,__REALTIME_TIMESTAMP,PRIORITY,MESSAGE,_SYSTEMD_UNIT,SYSLOG_IDENTIFIER", "--until=@" + fmt.Sprintf("%.6f", float64(q.Until)/1000)}
	if q.Cursor == "" {
		args = append(args, "--since=@"+strconv.FormatInt((q.Until-windowDuration(q).Milliseconds())/1000, 10))
	}
	switch q.Source {
	case "pilot":
		args = append(args, "-u", "lighten012-pilot.service", "-u", "pilot-netd.service")
	case "web":
		args = append(args, "-u", "lighten012-pilot.service")
	case "network":
		args = append(args, "-u", "pilot-netd.service")
	case "kernel":
		args = append(args, "_TRANSPORT=kernel")
	}
	if q.Level == "error" {
		args = append(args, "--priority=0..3")
	}
	if q.Level == "warning" {
		args = append(args, "--priority=0..4")
	}
	if q.Cursor != "" {
		args = append(args, "--cursor="+q.Cursor)
	}
	return args
}
func field(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	// Binary journal fields are represented as byte arrays. Duplicate fields may be arrays of strings.
	var b []uint8
	if json.Unmarshal(v, &b) == nil && b != nil {
		return strings.ToValidUTF8(string(b), "�")
	}
	var list []string
	if json.Unmarshal(v, &list) == nil {
		return strings.Join(list, "\n")
	}
	return ""
}
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "… [已截断]"
}
func Decode(data []byte, q Query) (Result, error) {
	result := Result{Entries: []Entry{}, Until: q.Until}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	last := ""
	needle := strings.ToLower(q.Search)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var obj map[string]json.RawMessage
		if e := json.Unmarshal(scanner.Bytes(), &obj); e != nil {
			return result, fmt.Errorf("日志格式无效: %w", e)
		}
		cursor := field(obj["__CURSOR"])
		if cursor == "" {
			return result, fmt.Errorf("日志缺少分页游标")
		}
		if cursor == q.Cursor {
			continue
		}
		ts, _ := strconv.ParseInt(field(obj["__REALTIME_TIMESTAMP"]), 10, 64)
		if q.Until > 0 && ts/1000 < q.Until-windowDuration(q).Milliseconds() {
			return result, nil
		}
		if result.Scanned >= 1000 || len(result.Entries) >= q.Limit {
			result.HasMore = true
			result.NextCursor = last
			break
		}
		result.Scanned++
		last = cursor
		message := field(obj["MESSAGE"])
		unit := field(obj["_SYSTEMD_UNIT"])
		if unit == "" {
			unit = field(obj["SYSLOG_IDENTIFIER"])
		}
		if needle != "" && !strings.Contains(strings.ToLower(message+" "+unit), needle) {
			continue
		}
		priority, e := strconv.Atoi(field(obj["PRIORITY"]))
		if e != nil || priority < 0 || priority > 7 {
			priority = 6
		}
		result.Entries = append(result.Entries, Entry{Cursor: cursor, Time: ts / 1000, Priority: priority, Unit: trim(unit, 256), Message: trim(message, 4096)})
	}
	if e := scanner.Err(); e != nil {
		return result, fmt.Errorf("日志记录过大: %w", e)
	}
	// A cursor itself consumes a line in journalctl's --lines limit.
	if result.Scanned >= 1000 && !result.HasMore {
		result.HasMore = true
		result.NextCursor = last
	}
	return result, nil
}

type bounded struct {
	buf   bytes.Buffer
	limit int
}

func (b *bounded) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("日志输出过大，请缩小时间范围")
	}
	return b.buf.Write(p)
}
func (b *bounded) Bytes() []byte { return b.buf.Bytes() }
func Read(ctx context.Context, q Query) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", Args(q)...)
	out := &bounded{limit: 8 * 1024 * 1024}
	errout := &bounded{limit: 8192}
	cmd.Stdout = out
	cmd.Stderr = errout
	if e := cmd.Run(); e != nil {
		return Result{}, fmt.Errorf("读取系统日志失败，请刷新或缩小范围: %w", e)
	}
	return Decode(out.Bytes(), q)
}

type Reader func(context.Context, Query) (Result, error)

func Handler(read Reader) http.Handler {
	slots := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fail := func(status int, e error) {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
		}
		if len(r.URL.RawQuery) > 8192 {
			fail(400, fmt.Errorf("查询参数过长"))
			return
		}
		q, e := Parse(r.URL.Query(), time.Now())
		if e != nil {
			fail(400, e)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			fail(429, fmt.Errorf("日志查询繁忙，请稍后重试"))
			return
		}
		result, e := read(r.Context(), q)
		if e != nil {
			fail(503, e)
			return
		}
		json.NewEncoder(w).Encode(result)
	})
}

var _ io.Writer = (*bounded)(nil)

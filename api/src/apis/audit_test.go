/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package apis

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"api/src/model"

	"github.com/gin-gonic/gin"
)

// 映射表的键写错（路由改名、参数名改动）时不会报错，只会让动态悄悄消失，这里对照真实路由校验
func TestAuditRoutesMatchRegisteredRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	registered := map[string]bool{}
	for _, r := range Register().Routes() {
		registered[r.Method+" "+strings.TrimPrefix(r.Path, "/api/v1")] = true
	}
	for key, route := range auditRoutes {
		if !registered[key] {
			t.Errorf("audit route %q is not a registered route", key)
		}
		if route.Param != "" && !strings.Contains(key, ":"+route.Param) {
			t.Errorf("audit route %q: param %q not in path", key, route.Param)
		}
		if !strings.HasPrefix(route.Action, route.Resource+".") {
			t.Errorf("audit route %q: action %q does not belong to resource %q", key, route.Action, route.Resource)
		}
	}
}

func TestResourceFromResponse(t *testing.T) {
	cases := []struct {
		body, id, name string
	}{
		{`{"id":"u1","name":"vpc-a"}`, "u1", "vpc-a"},
		// 实例的名称字段是 hostname
		{`{"id":"u2","hostname":"web-01","name":""}`, "u2", "web-01"},
		// hypers、flavors 的响应用 uuid 而不是 id
		{`{"uuid":"h1","hostname":"work-04","hostid":5}`, "h1", "work-04"},
		{`{"uuid":"f1","name":"small","cpu":2}`, "f1", "small"},
		// 两者都有时以 id 为准
		{`{"id":"i1","uuid":"x","name":"n"}`, "i1", "n"},
		// 单个元素的数组（批量创建接口）
		{`[{"id":"u3","hostname":"web-02"}]`, "u3", "web-02"},
		// 多个元素无法对应到单个资源
		{`[{"id":"a"},{"id":"b"}]`, "", ""},
		{`null`, "", ""},
		{``, "", ""},
		{`{"id":`, "", ""},
	}
	for _, c := range cases {
		id, name := resourceFromResponse(c.body)
		if id != c.id || name != c.name {
			t.Errorf("resourceFromResponse(%q) = (%q, %q), want (%q, %q)", c.body, id, name, c.id, c.name)
		}
	}
}

func timeRangeContext(query string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/v1/activities?"+query, nil)
	return c
}

func TestParseTimeRange(t *testing.T) {
	// 不传且无默认跨度：不限时间
	start, end, err := parseTimeRange(timeRangeContext(""), 0)
	if err != nil || !start.IsZero() || !end.IsZero() {
		t.Fatalf("expected unbounded range, got %v %v %v", start, end, err)
	}
	// 不传时取默认跨度
	start, end, err = parseTimeRange(timeRangeContext(""), activityDefaultSpan)
	if err != nil || end.Sub(start) != activityDefaultSpan {
		t.Fatalf("expected default span, got %v %v %v", start, end, err)
	}
	// 只传 end：start 往前推默认跨度
	start, end, err = parseTimeRange(timeRangeContext("end=2026-09-16T00:00:00Z"), activityDefaultSpan)
	if err != nil || !start.Equal(time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected start %v, err %v", start, err)
	}
	for _, bad := range []string{
		"start=yesterday",
		"end=2026-09-16",
		"start=2026-09-16T00:00:00Z&end=2026-09-16T00:00:00Z",
		"start=2026-09-17T00:00:00Z&end=2026-09-16T00:00:00Z",
		"start=2026-01-01T00:00:00Z&end=2026-09-16T00:00:00Z",
	} {
		if _, _, err = parseTimeRange(timeRangeContext(bad), activityDefaultSpan); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	// 只传 start 且无默认跨度：跨度超出上限也要拒绝
	if _, _, err = parseTimeRange(timeRangeContext("start=2020-01-01T00:00:00Z"), 0); err == nil {
		t.Errorf("expected error for start far in the past without end")
	}
}

func TestActivityCursorRoundTrip(t *testing.T) {
	created := time.Date(2026, 9, 16, 8, 30, 15, 123456000, time.UTC)
	cursor := encodeActivityCursor(&model.AuditLog{Model: model.Model{ID: 42, CreatedAt: created}})
	gotTime, gotID, err := decodeActivityCursor(cursor)
	if err != nil || gotID != 42 || !gotTime.Equal(created) {
		t.Fatalf("round trip failed: %v %d %v", gotTime, gotID, err)
	}
	for _, bad := range []string{"!!!", "bm9jb2xvbg", "YWJjOjEy"} {
		if _, _, err = decodeActivityCursor(bad); err == nil {
			t.Errorf("expected error for cursor %q", bad)
		}
	}
}

func TestTruncateUTF8(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 10, "abc"},
		{"abcdef", 3, "abc"},
		// "中" 占 3 字节：4 字节上限只能放下一个完整字符，不能留下半个
		{"中文", 4, "中"},
		{"中文", 5, "中"},
		{"中文", 6, "中文"},
		{"a中", 2, "a"},
		{"中", 0, ""},
		// 非法 UTF-8 字节（如路径中解码出的 %FF）被去掉
		{"ab\xffcd", 10, "abcd"},
	}
	for _, c := range cases {
		got := truncateUTF8(c.in, c.max)
		if got != c.want || !utf8.ValidString(got) || len(got) > c.max {
			t.Errorf("truncateUTF8(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestFitAuditColumns(t *testing.T) {
	entry := &model.AuditLog{
		Path:         "/api/v1/instances/" + strings.Repeat("x", 600),
		ResourceUUID: strings.Repeat("u", 100),
		ResourceName: strings.Repeat("名", 200),
		Detail:       strings.Repeat("错", 500),
	}
	fitAuditColumns(entry)
	checks := map[string]struct {
		value string
		limit int
	}{
		"path":          {entry.Path, 512},
		"resource_uuid": {entry.ResourceUUID, 64},
		"resource_name": {entry.ResourceName, 255},
		"detail":        {entry.Detail, auditDetailLimit},
	}
	for field, c := range checks {
		if len(c.value) > c.limit || !utf8.ValidString(c.value) {
			t.Errorf("%s not fitted: len=%d limit=%d valid=%v", field, len(c.value), c.limit, utf8.ValidString(c.value))
		}
	}
	// 截断后应尽量占满列宽，而不是被截得过短
	if len(entry.ResourceName) < 255-2 {
		t.Errorf("resource_name over-truncated to %d bytes", len(entry.ResourceName))
	}
}

func TestAuditBodyWriterTruncates(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	w := &auditBodyWriter{ResponseWriter: c.Writer}
	chunk := strings.Repeat("x", auditBodyLimit-10)
	w.Write([]byte(chunk))
	if w.truncated {
		t.Fatalf("should not be truncated yet")
	}
	w.Write([]byte(strings.Repeat("y", 20)))
	if !w.truncated || w.body.Len() != auditBodyLimit {
		t.Fatalf("expected truncation at %d, got truncated=%v len=%d", auditBodyLimit, w.truncated, w.body.Len())
	}
}

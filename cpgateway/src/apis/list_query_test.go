package apis

import (
	"fmt"
	"testing"

	"cpgateway/src/dbs"
	"cpgateway/src/model"
)

// 网关的列表接口原先一次全量返回、前端自己过滤。加上分页和搜索后，这里守住三件事：
// total 是过滤后的总数而不是当前页条数、offset/limit 真的生效、搜索走服务端且大小写无关。
func TestOrgListPaginationAndSearch(t *testing.T) {
	e := newSecEnv(t, nil)
	owner := e.user("lister")
	// 名字里混进大小写和通配符字符，一并验证搜索不区分大小写、`%` 不被当成通配符
	created := make([]*model.Organization, 0, 8)
	for i := 1; i <= 7; i++ {
		created = append(created, e.org(fmt.Sprintf("Page%02d", i), owner))
	}
	special := e.org("Pct100%off", owner)
	created = append(created, special)

	// 这个文件按字母序排在 parity_test.go 前面、先跑，而 parity 断言的是**全局**的
	// 区域列表和组织列表，所以这里建的数据必须真删掉（newSecEnv 的清理只把区域置为不可用）
	t.Cleanup(func() {
		// 用原生 SQL：GORM 的删除回调会去解析模型关联，这里只是想把行抹掉
		db := dbs.DB()
		for _, o := range created {
			for _, table := range []string{"members", "org_resource_quotas", "org_resource_consumptions"} {
				db.Exec("DELETE FROM "+table+" WHERE org_id = ?", o.ID)
			}
			db.Exec("DELETE FROM organizations WHERE id = ?", o.ID)
		}
		db.Exec("DELETE FROM users WHERE id = ?", owner.ID)
		db.Exec("DELETE FROM regions WHERE id = ?", e.region.ID)
	})
	tok := e.token(owner, special, 0)

	all := e.c.expect("GET", "/api/v1/orgs?limit=100", tok, nil, 200)
	total, _ := all["total"].(float64)
	if int(total) != 8 {
		t.Fatalf("total should count every org the user belongs to, got %v", all["total"])
	}

	// 第一页 3 条，total 仍然是 8（不是当前页条数）
	first := e.c.expect("GET", "/api/v1/orgs?offset=0&limit=3", tok, nil, 200)
	firstOrgs, _ := first["orgs"].([]interface{})
	if first["total"] != total || len(firstOrgs) != 3 {
		t.Fatalf("first page: total=%v len=%d", first["total"], len(firstOrgs))
	}
	// 翻页拿到的是不同的记录
	second := e.c.expect("GET", "/api/v1/orgs?offset=3&limit=3", tok, nil, 200)
	secondOrgs, _ := second["orgs"].([]interface{})
	if len(secondOrgs) != 3 {
		t.Fatalf("second page len=%d", len(secondOrgs))
	}
	firstUUID := firstOrgs[0].(map[string]interface{})["uuid"]
	for _, o := range secondOrgs {
		if o.(map[string]interface{})["uuid"] == firstUUID {
			t.Fatalf("offset did not advance: %v repeated", firstUUID)
		}
	}

	// limit 超过上限时收敛，不会一次把整张表捞出来
	capped := e.c.expect("GET", "/api/v1/orgs?limit=100000", tok, nil, 200)
	cappedOrgs, _ := capped["orgs"].([]interface{})
	if len(cappedOrgs) > maxListLimit {
		t.Fatalf("limit not capped: %d", len(cappedOrgs))
	}

	// 搜索在服务端做，大小写无关
	found := e.c.expect("GET", "/api/v1/orgs?query="+e.name("page01"), tok, nil, 200)
	foundOrgs, _ := found["orgs"].([]interface{})
	if found["total"] != float64(1) || len(foundOrgs) != 1 {
		t.Fatalf("case-insensitive search: %v", found)
	}

	// `%` 是字面量而不是通配符：搜 "100%" 只能命中那一个组织
	pct := e.c.expect("GET", "/api/v1/orgs?query=100%25", tok, nil, 200)
	pctOrgs, _ := pct["orgs"].([]interface{})
	if pct["total"] != float64(1) || len(pctOrgs) != 1 ||
		pctOrgs[0].(map[string]interface{})["uuid"] != special.UUID {
		t.Fatalf("%% must be escaped, got %v", pct)
	}

	// 搜不到就是空列表 + total 0，不是把全部返回回去
	none := e.c.expect("GET", "/api/v1/orgs?query=no-such-org-"+e.suffix, tok, nil, 200)
	noneOrgs, _ := none["orgs"].([]interface{})
	if none["total"] != float64(0) || len(noneOrgs) != 0 {
		t.Fatalf("empty search should return nothing: %v", none)
	}
}

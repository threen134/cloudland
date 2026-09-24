package apis

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 列表接口的分页与搜索。网关这几个列表（组织、用户、区域、通知渠道）原先一次全量返回，
// 前端拿到后自己过滤：数据一多就是把整张表搬到浏览器里，而且和 clapi 那边「分页、搜索、
// 排序一律在服务端做」的约定不一致。
//
// 约定与 clapi 对齐：offset / limit / query 三个参数，返回 {total, <资源>}，
// total 是过滤之后的总数（不是当前页的条数）。
const (
	defaultListLimit = 50
	maxListLimit     = 500
)

type listParams struct {
	Offset int
	Limit  int
	Query  string
}

func parseListParams(c *gin.Context) (listParams, bool) {
	offset, ok := queryInt(c, "offset", 0)
	if !ok {
		return listParams{}, false
	}
	limit, ok := queryInt(c, "limit", defaultListLimit)
	if !ok {
		return listParams{}, false
	}
	if offset < 0 {
		offset = 0
	}
	// limit=0 当成「用默认值」，负数和超大值都收敛到区间内：前端偶尔会传 0
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return listParams{Offset: offset, Limit: limit, Query: strings.TrimSpace(c.Query("query"))}, true
}

// searchScope 生成对若干列的模糊匹配。`%` `_` `\` 会被转义，避免用户输入的通配符改变语义
// （clapi 那边是 dbs.Contains，这里是同一个意思）。
func searchScope(query string, columns ...string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if query == "" || len(columns) == 0 {
			return db
		}
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(query))
		pattern := "%" + escaped + "%"
		conds := make([]string, 0, len(columns))
		args := make([]interface{}, 0, len(columns))
		for _, col := range columns {
			// 用 LOWER(...) LIKE 而不是 PostgreSQL 的 ILIKE：测试默认跑内存 SQLite，那边没有 ILIKE
			conds = append(conds, "LOWER("+col+") LIKE ? ESCAPE '\\'")
			args = append(args, pattern)
		}
		return db.Where(strings.Join(conds, " OR "), args...)
	}
}

// countAndPage 先数总数再取当前页。q 必须是已经带好过滤条件的查询。
//
// 两个坑：
//   - 计数要用 Session 复制一份。GORM 的链式调用共用同一个 Statement，直接在 q 上
//     Count 之后再 Find，条件会累积到同一条语句上。
//   - 计数前要清掉 Select。组织列表为了 join 用了 Select("organizations.*")，
//     Count 会照着拼出 COUNT(organizations.*)，PostgreSQL 和 SQLite 都报错。
func countAndPage(c *gin.Context, q *gorm.DB, p listParams, dest interface{}) (int64, bool) {
	var total int64
	if err := q.Session(&gorm.Session{}).Select("*").Count(&total).Error; err != nil {
		internalServerError(c, err)
		return 0, false
	}
	if err := q.Session(&gorm.Session{}).Offset(p.Offset).Limit(p.Limit).Find(dest).Error; err != nil {
		internalServerError(c, err)
		return 0, false
	}
	return total, true
}


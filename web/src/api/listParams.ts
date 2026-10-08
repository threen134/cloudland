/**
 * 网关侧列表接口的分页与搜索参数（cpgateway 的 apis/list_query.go）。
 * 与 clapi 那边的约定一致：offset / limit / query，返回 { total, <资源> }。
 * limit 不传时后端用 50，上限 500。
 */
export interface ListParams {
    offset?: number
    limit?: number
    query?: string
    /**
     * Sort column, `name` ascending or `-name` descending. Each gateway list has its own whitelist (orgs: name,
     * slug, status, created_at; users and members: username, email, created_at, and a few more); other values are
     * ignored and the list keeps its default order
     */
    order?: string
}

/**
 * limit for the lists behind a dropdown or a picker. Without it the APIs return their default page of 50 and
 * the options past it are silently missing. 500 is the cap of the gateway lists (clapi has none).
 */
export const OPTION_LIST_LIMIT = 500

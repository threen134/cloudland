/**
 * 网关侧列表接口的分页与搜索参数（cpgateway 的 apis/list_query.go）。
 * 与 clapi 那边的约定一致：offset / limit / query，返回 { total, <资源> }。
 * limit 不传时后端用 50，上限 500。
 */
export interface ListParams {
    offset?: number
    limit?: number
    query?: string
}

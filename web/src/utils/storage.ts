// 浏览器存储的键名。原先这 7 个键以字面量散落在 6 个文件里，改名或清理时很容易漏掉一处——
// 登出没清干净就会串到下一个账号。
//
// 存哪儿由「是否勾了记住我」决定：勾了写 localStorage（跨会话保留），没勾写 sessionStorage
// （每个标签页一份，关掉就没）。读取一律两个都查，见 api/client.ts 的 getStoredToken。
export const STORAGE_KEYS = {
    /** 访问令牌（JWT） */
    token: 'cloudland_token',
    /** 当前登录用户，JSON */
    user: 'cloudland_user',
    /** 勾了「记住我」的标记，值固定是 '1' */
    remember: 'cloudland_remember',
    /** 当前组织 ID，随请求发 X-Organization-ID */
    orgId: 'cloudland_org_id',
    /** 当前区域 UUID，随请求发 region 查询参数 */
    regionUuid: 'cloudland_region_uuid',
    /** 当前区域 UUID 的副本，区域 store 自己用（与 regionUuid 同值，历史遗留的两个键） */
    regionId: 'cloudland_region_id',
    /** 连续登录失败次数，达到阈值后要求验证码 */
    loginAttempts: 'cloudland_login_attempts',
    /** 手动选择的界面语言 */
    language: 'cloudland_language',
} as const

export type StorageKey = (typeof STORAGE_KEYS)[keyof typeof STORAGE_KEYS]

/** 登出时清掉所有登录态（两种存储都清，避免"没勾记住我"的残留） */
export const clearAuthStorage = () => {
    for (const key of [
        STORAGE_KEYS.token,
        STORAGE_KEYS.user,
        STORAGE_KEYS.remember,
        STORAGE_KEYS.orgId,
        STORAGE_KEYS.regionUuid,
        STORAGE_KEYS.regionId,
    ]) {
        localStorage.removeItem(key)
        sessionStorage.removeItem(key)
    }
}

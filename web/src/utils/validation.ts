export const NAME_REGEX = /^[a-zA-Z][a-zA-Z0-9_-]*$/

/**
 * 资源名长度：后端对 VPC、子网、安全组、安全组规则、负载均衡、监听器、后端等
 * 一律是 `binding:"min=2,max=32"`（子网是 max=64）。前端原先只校验字符集，
 * 1 个字符或超长的名字会被放行，直到后端返回未本地化的 gin 原始报错。
 */
export const NAME_MIN_LENGTH = 2
export const NAME_MAX_LENGTH = 32

/** 空串按「还没填」处理，返回 true：各表单自己判必填 */
export const isValidName = (name: string, maxLength: number = NAME_MAX_LENGTH): boolean => {
    if (!name) return true
    if (name.length < NAME_MIN_LENGTH || name.length > maxLength) return false
    return NAME_REGEX.test(name)
}

/**
 * Instance hostname. clapi binds it as `hostname|fqdn` (go-playground validator v10.30): an RFC 952 host name
 * (starts with a letter, labels of letters, digits and inner hyphens joined by dots) or an RFC 1123 FQDN
 * (labels may start with a digit, a non numeric top level label, optional trailing dot). No underscores, and no
 * label may end with a hyphen. The two patterns are copied from the validator so the form refuses exactly what
 * the API refuses; the 2-32 length matches the other resource names.
 */
const HOSTNAME_RFC952 = /^[a-zA-Z]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$/
const FQDN_RFC1123 =
    /^([a-zA-Z0-9][a-zA-Z0-9-]{0,62})(\.[a-zA-Z0-9][a-zA-Z0-9-]{0,62})*?(\.[a-zA-Z][a-zA-Z0-9-]{0,62})\.?$/

/** Empty counts as "not filled in yet" and returns true, like isValidName: each form checks required itself */
export const isValidHostname = (name: string, maxLength: number = NAME_MAX_LENGTH): boolean => {
    if (!name) return true
    if (name.length < NAME_MIN_LENGTH || name.length > maxLength) return false
    return HOSTNAME_RFC952.test(name) || FQDN_RFC1123.test(name)
}

/**
 * Longest hostname a create request may carry: with count > 1 clapi names the instances <hostname>-1 ..
 * <hostname>-<count>, and each name must still be at most NAME_MAX_LENGTH long (a rename checks it again)
 */
export const hostnameMaxLength = (count: number): number =>
    count > 1 ? NAME_MAX_LENGTH - `-${Math.floor(count)}`.length : NAME_MAX_LENGTH

/** CIDR（IPv4），例如 10.0.0.0/8；后端 `binding:"cidrv4"` 的等价校验 */
export const isValidCIDRv4 = (cidr: string): boolean => {
    const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(cidr)
    if (!match) return false
    const octets = match.slice(1, 5).map(Number)
    if (octets.some((n) => n > 255)) return false
    const prefix = Number(match[5])
    return prefix >= 0 && prefix <= 32
}

/** A comma separated CIDR list holds a default route (any x.x.x.x/0): clapi refuses it in the VPN client routes */
export const hasDefaultRoute = (list: string): boolean => list.split(',').some((cidr) => /\/0+$/.test(cidr.trim()))

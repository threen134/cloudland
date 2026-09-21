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

/** CIDR（IPv4），例如 10.0.0.0/8；后端 `binding:"cidrv4"` 的等价校验 */
export const isValidCIDRv4 = (cidr: string): boolean => {
    const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(cidr)
    if (!match) return false
    const octets = match.slice(1, 5).map(Number)
    if (octets.some((n) => n > 255)) return false
    const prefix = Number(match[5])
    return prefix >= 0 && prefix <= 32
}

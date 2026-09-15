import type { SecurityRule, SecurityRulePayload } from '../api/networks'

type Translate = (key: string, named?: Record<string, unknown>) => string

/**
 * 安全组规则表单：tcp/udp 使用端口范围（两个都留空表示全部端口，只填一个表示单端口）；
 * icmp 使用 type/code（留空表示任意）。后端对 icmp 规则复用 port_min/port_max 存 type/code，-1 表示任意。
 * v-model.number 清空输入框时得到 ''，所以数值字段都允许 ''。
 */
export interface SecurityRuleForm {
    name: string
    direction: 'ingress' | 'egress'
    protocol: 'tcp' | 'udp' | 'icmp'
    port_min: number | ''
    port_max: number | ''
    icmp_type: number | ''
    icmp_code: number | ''
    remote_cidr: string
}

export const newSecurityRuleForm = (): SecurityRuleForm => ({
    name: '',
    direction: 'ingress',
    protocol: 'tcp',
    port_min: 80,
    port_max: 80,
    icmp_type: '',
    icmp_code: '',
    remote_cidr: '0.0.0.0/0'
})

export const securityRuleFormFromRule = (rule: SecurityRule): SecurityRuleForm => {
    const isIcmp = rule.protocol === 'icmp'
    return {
        name: rule.name || '',
        direction: rule.direction,
        protocol: rule.protocol,
        port_min: !isIcmp && rule.port_min && rule.port_min > 0 ? rule.port_min : 1,
        port_max: !isIcmp && rule.port_max && rule.port_max > 0 ? rule.port_max : 65535,
        icmp_type: isIcmp && rule.port_min != null && rule.port_min >= 0 ? rule.port_min : '',
        icmp_code: isIcmp && rule.port_max != null && rule.port_max >= 0 ? rule.port_max : '',
        remote_cidr: rule.remote_cidr || ''
    }
}

const isBlank = (v: number | '' | null | undefined): v is '' | null | undefined =>
    v === '' || v == null || Number.isNaN(v)

const inRange = (v: number | '', min: number, max: number) =>
    typeof v === 'number' && Number.isInteger(v) && v >= min && v <= max

/** 返回错误提示，校验通过返回空串 */
export const validateSecurityRuleForm = (form: SecurityRuleForm, t: Translate): string => {
    if (form.protocol === 'icmp') {
        const hasType = !isBlank(form.icmp_type)
        const hasCode = !isBlank(form.icmp_code)
        if (hasType && !inRange(form.icmp_type, 0, 254)) {
            return t('dashboard.securityGroupDetail.icmpTypeRangeError')
        }
        if (hasCode && !inRange(form.icmp_code, 0, 255)) {
            return t('dashboard.securityGroupDetail.icmpCodeRangeError')
        }
        if (hasCode && !hasType) {
            return t('dashboard.securityGroupDetail.icmpCodeRequiresType')
        }
        return ''
    }
    const hasMin = !isBlank(form.port_min)
    const hasMax = !isBlank(form.port_max)
    if ((hasMin && !inRange(form.port_min, 1, 65535)) || (hasMax && !inRange(form.port_max, 1, 65535))) {
        return t('dashboard.securityGroupDetail.portValueError')
    }
    if (hasMin && hasMax && (form.port_min as number) > (form.port_max as number)) {
        return t('dashboard.securityGroupDetail.portRangeError')
    }
    return ''
}

/** 生成请求参数：留空的端口不发送（tcp/udp 由后端视为全部端口或单端口，icmp 发送 -1 表示任意） */
export const securityRulePayloadFromForm = (form: SecurityRuleForm): SecurityRulePayload => {
    const payload: SecurityRulePayload = {
        name: form.name,
        direction: form.direction,
        protocol: form.protocol,
        remote_cidr: form.remote_cidr
    }
    if (form.protocol === 'icmp') {
        payload.port_min = isBlank(form.icmp_type) ? -1 : Number(form.icmp_type)
        payload.port_max = isBlank(form.icmp_code) ? -1 : Number(form.icmp_code)
    } else {
        if (!isBlank(form.port_min)) payload.port_min = Number(form.port_min)
        if (!isBlank(form.port_max)) payload.port_max = Number(form.port_max)
    }
    return payload
}

const ICMP_TYPE_NAMES: Record<number, string> = {
    0: 'Echo Reply',
    3: 'Dest Unreachable',
    5: 'Redirect',
    8: 'Echo Request',
    11: 'Time Exceeded',
}

/** ICMP 规则的 type/code 展示文本；非 ICMP 规则返回 null */
export const formatIcmpRule = (rule: SecurityRule, t: Translate): string | null => {
    if (rule.protocol !== 'icmp') return null
    const type = rule.port_min ?? -1
    const code = rule.port_max ?? -1
    if (type < 0) return t('dashboard.forms.placeholder.all')
    const typeText = t('dashboard.securityGroupDetail.icmpTypeValue', { type })
    return code < 0 ? typeText : `${typeText} / ${t('dashboard.securityGroupDetail.icmpCodeValue', { code })}`
}

/** 常见 ICMP type 的名称标签；非 ICMP 或任意 type 返回 null */
export const icmpTypeName = (rule: SecurityRule): string | null => {
    if (rule.protocol !== 'icmp' || rule.port_min == null || rule.port_min < 0) return null
    return ICMP_TYPE_NAMES[rule.port_min] ?? null
}

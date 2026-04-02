import { useI18n } from 'vue-i18n'

export function useSecurityGroup() {
    const { t } = useI18n()

    const translateDescription = (description: string) => {
        if (!description) return description
        if (description === 'System default security group') {
            return t('dashboard.securityGroupDetail.systemDefaultDescription')
        }
        const nativePrefix = 'Native security group for router '
        if (description.startsWith(nativePrefix)) {
            const vpcName = description.substring(nativePrefix.length)
            return t('dashboard.securityGroupDetail.nativeForRouterDescription', { name: vpcName })
        }
        return description
    }

    return { translateDescription }
}

import type { Instance } from '../api/instances'

/** Private address of the primary interface without the prefix length ("192.168.1.5", not "192.168.1.5/24") */
export const primaryIp = (inst: Instance): string | undefined =>
    inst.interfaces?.find((iface) => iface.is_primary)?.ip_address?.split('/')[0] || undefined

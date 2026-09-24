<script setup lang="ts">
// Action sentence of an activity with the resource name rendered as a link, e.g. "deleted security group web-sg".
// The actor is not included, so the overview card and the activity table can lay it out differently.
import { useI18n } from 'vue-i18n'
import type { Activity } from '../../api/activities'

const props = defineProps<{ activity: Activity }>()

const { te } = useI18n()

// Resource type -> detail route. Types without a detail page (keys, flavors) show the name only.
const detailRoutes: Record<string, string> = {
    instance: 'instance-detail',
    volume: 'volume-detail',
    vpc: 'vpc-detail',
    subnet: 'subnet-detail',
    security_group: 'security-group-detail',
    floating_ip: 'floating-ip-detail',
    load_balancer: 'load-balancer-detail',
    vpn_gateway: 'vpn-gateway-detail',
    image: 'image-detail',
    hyper: 'hypervisor-detail',
    migration: 'migration-detail',
}

// {name} in the message is rendered by a slot as the (linked) resource name. Actions added on the backend
// but not yet translated fall back to a generic message.
// Failed actions use a separate message set: success messages are in completed form ("deleted ..."),
// which contradicts a failed result.
const textKey = () => {
    const a = props.activity
    const key = `dashboard.overview.${a.success ? 'activityActions' : 'activityActionsFailed'}.${a.action}`
    if (te(key)) return key
    return a.success ? 'dashboard.overview.activityActionUnknown' : 'dashboard.overview.activityActionUnknownFailed'
}

const resourceLabel = () => {
    const a = props.activity
    return a.resource_name || (a.resource_id ? a.resource_id.slice(0, 8) : '')
}

// No link for deleted or failed actions: the detail page most likely does not exist.
const resourceLink = () => {
    const a = props.activity
    if (!a.success || a.action.endsWith('.delete')) return null
    if (a.resource_type === 'zone' && a.resource_name) {
        return { name: 'zone-detail', params: { name: a.resource_name } }
    }
    const routeName = detailRoutes[a.resource_type]
    if (!routeName || !a.resource_id) return null
    return { name: routeName, params: { id: a.resource_id } }
}
</script>

<template>
    <i18n-t :keypath="textKey()" tag="span">
        <template #action>{{ activity.action }}</template>
        <template #name>
            <router-link
                v-if="resourceLink()"
                :to="resourceLink()!"
                class="activity-resource"
                :title="resourceLabel()"
                >{{ resourceLabel() }}</router-link
            >
            <span v-else class="activity-resource plain" :title="resourceLabel()">{{ resourceLabel() }}</span>
        </template>
    </i18n-t>
</template>

<style scoped>
/* Resource names can be long (each resource sets its own limit; the audit column holds up to 255 chars): truncate to one line, full name on hover. */
.activity-resource {
    display: inline-block;
    max-width: min(16em, 100%);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: bottom;
    font-weight: 500;
    color: var(--primary-600);
    text-decoration: none;
}

.activity-resource:hover {
    text-decoration: underline;
}

.activity-resource.plain {
    color: var(--gray-900);
}
</style>

<script setup lang="ts">
// A single activity entry: actor + action text (with a link to the resource) + failed badge + time.
// Shared by the Recent Activity card on the overview page and the full activity page.
import { type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { Server, HardDrive, Cpu, Layers, Disc, GitFork, Activity, Globe, Shield, Key, ArrowRightLeft, Network, Archive, MapPin, Box } from 'lucide-vue-next'
import type { Activity as ActivityItem } from '../../api/activities'

const props = withDefaults(defineProps<{
    activity: ActivityItem
    // The full list also shows the absolute time; the card shows relative time only (absolute on hover).
    showAbsoluteTime?: boolean
}>(), {
    showAbsoluteTime: false,
})

const { t, te } = useI18n()

const icons: Record<string, Component> = {
    instance: Server,
    volume: HardDrive,
    backup: Archive,
    consistency_group: HardDrive,
    vpc: Layers,
    subnet: GitFork,
    security_group: Shield,
    floating_ip: Globe,
    load_balancer: Network,
    image: Disc,
    key: Key,
    flavor: Box,
    hyper: Cpu,
    zone: MapPin,
    migration: ArrowRightLeft,
}
const iconClass: Record<string, string> = {
    instance: 'bg-blue-light text-blue',
    volume: 'bg-teal-light text-teal',
    backup: 'bg-teal-light text-teal',
    consistency_group: 'bg-teal-light text-teal',
    vpc: 'bg-purple-light text-purple',
    subnet: 'bg-purple-light text-purple',
    security_group: 'bg-rose-light text-rose',
    floating_ip: 'bg-blue-light text-blue',
    load_balancer: 'bg-purple-light text-purple',
    image: 'bg-rose-light text-rose',
}

// Resource type -> detail route. Types without a detail page (keys, flavors) show the name only.
const detailRoutes: Record<string, string> = {
    instance: 'instance-detail',
    volume: 'volume-detail',
    vpc: 'vpc-detail',
    subnet: 'subnet-detail',
    security_group: 'security-group-detail',
    floating_ip: 'floating-ip-detail',
    load_balancer: 'load-balancer-detail',
    image: 'image-detail',
    hyper: 'hypervisor-detail',
    migration: 'migration-detail',
}

// {name} in the message is rendered by a slot as the (linked) resource name. Actions added on the backend
// but not yet translated fall back to a generic message.
// Failed actions use a separate message set: success messages are in completed form ("deleted ..."),
// which contradicts a trailing "failed" badge.
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

const relativeTime = (iso: string) => {
    const mins = Math.floor((Date.now() - new Date(iso).getTime()) / 60000)
    if (mins < 1) return t('dashboard.overview.justNow')
    if (mins < 60) return t('dashboard.overview.minsAgo', { n: mins })
    const hours = Math.floor(mins / 60)
    if (hours < 24) return t('dashboard.overview.hoursAgo', { n: hours })
    return t('dashboard.overview.daysAgo', { n: Math.floor(hours / 24) })
}

const absoluteTime = (iso: string) => new Date(iso).toLocaleString()
</script>

<template>
  <li class="activity-item">
    <div class="activity-icon" :class="iconClass[activity.resource_type] || 'bg-gray-light text-gray'">
      <component :is="icons[activity.resource_type] || Activity" :size="16" />
    </div>
    <div class="activity-details">
      <span class="activity-text">
        <strong>{{ activity.actor || $t('dashboard.overview.activityUnknownActor') }}</strong>
        {{ ' ' }}
        <i18n-t :keypath="textKey()" tag="span">
          <template #action>{{ activity.action }}</template>
          <template #name>
            <router-link v-if="resourceLink()" :to="resourceLink()!" class="activity-resource" :title="resourceLabel()">{{ resourceLabel() }}</router-link>
            <span v-else class="activity-resource plain" :title="resourceLabel()">{{ resourceLabel() }}</span>
          </template>
        </i18n-t>
        <span v-if="!activity.success" class="activity-failed">{{ $t('dashboard.overview.activityFailed') }}</span>
      </span>
      <span class="activity-time" :title="absoluteTime(activity.created_at)">
        {{ relativeTime(activity.created_at) }}<template v-if="showAbsoluteTime"> · {{ absoluteTime(activity.created_at) }}</template>
      </span>
    </div>
  </li>
</template>

<style scoped>
.activity-item {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px 0;
  border-bottom: 1px solid var(--border-light);
}

.activity-item:last-child {
  border-bottom: none;
  padding-bottom: 0;
}

.activity-item:first-child {
  padding-top: 0;
}

.activity-icon {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.bg-blue-light { background-color: var(--primary-100); }
.text-blue { color: var(--primary-600); }
.bg-teal-light { background-color: var(--accent-teal-light); }
.text-teal { color: var(--accent-teal); }
.bg-purple-light { background-color: var(--accent-purple-light); }
.text-purple { color: var(--accent-purple); }
.bg-rose-light { background-color: var(--accent-rose-light); }
.text-rose { color: var(--accent-rose); }
.bg-gray-light { background-color: var(--gray-100); }
.text-gray { color: var(--gray-500); }

.activity-details {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.activity-text {
  font-size: 0.9375rem;
  color: var(--gray-700);
  margin-bottom: 2px;
  overflow-wrap: anywhere;
}

.activity-text strong {
  color: var(--gray-900);
  font-weight: 600;
}

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

.activity-failed {
  display: inline-block;
  vertical-align: bottom;
  margin-left: 6px;
  padding: 0 6px;
  font-size: 0.75rem;
  line-height: 1.25rem;
  border-radius: 999px;
  color: var(--error-color);
  background-color: color-mix(in srgb, var(--error-color) 12%, transparent);
}

.activity-time {
  font-size: 0.75rem;
  color: var(--gray-400);
}
</style>

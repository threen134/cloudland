<script setup lang="ts">
// A single activity entry in feed style: icon + actor + action sentence + failed badge + relative time.
// Used by the Recent Activity card on the overview page; the full activity page renders a table instead.
import { type Component } from 'vue'
import { Server, HardDrive, Cpu, Layers, Disc, GitFork, Activity, Globe, Shield, Key, ArrowRightLeft, Network, Archive, MapPin, Box } from 'lucide-vue-next'
import type { Activity as ActivityItem } from '../../api/activities'
import ActivityText from './ActivityText.vue'
import { useActivityTime } from './activityTime'

defineProps<{ activity: ActivityItem }>()

const { relativeTime, absoluteTime } = useActivityTime()

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
        <ActivityText :activity="activity" />
        <span v-if="!activity.success" class="activity-failed">{{ $t('dashboard.overview.activityFailed') }}</span>
      </span>
      <span class="activity-time" :title="absoluteTime(activity.created_at)">{{ relativeTime(activity.created_at) }}</span>
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

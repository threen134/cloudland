<script setup lang="ts">
import { ref, computed, onMounted, type Component } from 'vue'
import { useAuthStore } from '../../stores/auth'
import { useRegionStore } from '../../stores/region'
import { useI18n } from 'vue-i18n'
import { Server, HardDrive, Cpu, Layers, Disc, GitFork, Activity, Globe, Shield, Key, ArrowRightLeft, Network, Archive, MapPin, Box } from 'lucide-vue-next'

import { instancesApi, type Instance } from '../../api/instances'
import { activitiesApi, type Activity as ActivityItem } from '../../api/activities'
import { volumesApi } from '../../api/volumes'
import { imagesApi, type Image } from '../../api/images'
import { vpcsApi, floatingIpsApi } from '../../api/networks'
import { quotaApi } from '../../api/quota'

const auth = useAuthStore()
const regionStore = useRegionStore()
const { t, te } = useI18n()
const displayName = computed(() => auth.user?.username || auth.user?.name || 'User')

interface ResourceUsage {
    used: number
    total: number
    unit?: string
    percentage: number
}

interface SystemStats {
    cpu: ResourceUsage
    memory: ResourceUsage
    disk: ResourceUsage
    instances: ResourceUsage
    volume: ResourceUsage
    images: ResourceUsage
    public_ip: ResourceUsage
    private_ip: ResourceUsage
}

const loading = ref(true)
const stats = ref<SystemStats | null>(null)

// Default empty data pattern (as fallback)
const emptyStats: SystemStats = {
    cpu: { used: 0, total: 0, percentage: 0 },
    memory: { used: 0, total: 0, unit: 'GB', percentage: 0 },
    disk: { used: 0, total: 0, unit: 'GB', percentage: 0 },
    instances: { used: 0, total: 0, percentage: 0 },
    volume: { used: 0, total: 0, unit: 'GB', percentage: 0 },
    images: { used: 0, total: 0, percentage: 0 },
    public_ip: { used: 0, total: 0, percentage: 0 },
    private_ip: { used: 0, total: 0, percentage: 0 }
}

onMounted(async () => {
    try {
        // Fetch resource data and quota in parallel
        const orgUuid = auth.user?.current_org_uuid || ''
        const regionUuid = regionStore.currentRegionId || ''

        const [instRes, volRes, imgRes, vpcRes, fipRes, quotaRes] = await Promise.all([
            instancesApi.fetchInstances().catch(err => { console.warn('Instances fetch failed:', err); return { data: [] } }),
            volumesApi.list({ limit: 100, type: 'all' }).catch(err => { console.warn('Volumes fetch failed:', err); return { volumes: [] } }),
            imagesApi.fetchImages().catch(err => { console.warn('Images fetch failed:', err); return { data: [] } }),
            vpcsApi.list({ limit: 100 }).catch(err => { console.warn('VPCs fetch failed:', err); return { vpcs: [] } }),
            floatingIpsApi.list({ limit: 100 }).catch(err => { console.warn('FIPs fetch failed:', err); return { floating_ips: [] } }),
            (orgUuid && regionUuid)
                ? quotaApi.getOrgRegionResourceInfo(orgUuid, regionUuid).catch(err => { console.warn('Quota fetch failed:', err); return null })
                : Promise.resolve(null),
        ])

        const instances = instRes.data?.instances || instRes.data || []
        const volumes = volRes.volumes || []
        const images = imgRes.data?.images || imgRes.data || []
        const vpcs = vpcRes.vpcs || []
        const fips = fipRes.floating_ips || []

        // Get quota and consumption from API (fallback to client-side calculation)
        const quota = quotaRes?.data?.quota
        const consumption = quotaRes?.data?.consumption

        // Use server-side consumption when available, otherwise compute from list data
        const usedCpu = consumption?.cpu_cores ?? (Array.isArray(instances) ? instances.reduce((acc: number, inst: Instance) => acc + (inst.flavor?.cpu || 0), 0) : 0)
        const usedMemGB = consumption?.ram_gb ?? (Array.isArray(instances) ? instances.reduce((acc: number, inst: Instance) => acc + (inst.flavor?.memory || 0), 0) / 1024 : 0)
        const usedDiskGB = consumption?.disk_gb ?? (Array.isArray(volumes) ? volumes.reduce((acc: number, vol: any) => acc + (vol.size || 0), 0) : 0)
        const usedPublicIps = consumption?.public_ips ?? (Array.isArray(fips) ? fips.length : 0)

        const totalCpu = quota?.max_cpu_cores ?? 64
        const totalMemGB = quota?.max_ram_gb ?? 128
        const totalDiskGB = quota?.max_disk_gb ?? 2000
        const totalPublicIps = quota?.max_public_ips ?? 20

        const instanceCount = Array.isArray(instances) ? instances.length : 0
        const imageCount = Array.isArray(images) ? images.length : 0
        const vpcCount = Array.isArray(vpcs) ? vpcs.length : 0

        const safePercent = (used: number, total: number) => total > 0 ? Math.min(Math.round((used / total) * 100), 100) : 0

        stats.value = {
            cpu: { used: usedCpu, total: totalCpu, percentage: safePercent(usedCpu, totalCpu) },
            memory: { used: Math.round(usedMemGB), total: totalMemGB, unit: 'GB', percentage: safePercent(usedMemGB, totalMemGB) },
            disk: { used: usedDiskGB, total: totalDiskGB, unit: 'GB', percentage: safePercent(usedDiskGB, totalDiskGB) },
            instances: { used: instanceCount, total: 0, percentage: 0 },
            volume: { used: usedDiskGB, total: totalDiskGB, unit: 'GB', percentage: safePercent(usedDiskGB, totalDiskGB) },
            images: { used: imageCount, total: 0, percentage: 0 },
            public_ip: { used: usedPublicIps, total: totalPublicIps, percentage: safePercent(usedPublicIps, totalPublicIps) },
            private_ip: { used: vpcCount, total: 0, percentage: 0 }
        }
    } catch (error) {
        console.error('Failed to fetch actual stats:', error)
        stats.value = emptyStats
    } finally {
        loading.value = false
    }
})

// ---- 最近动态：当前组织在当前区域的操作记录 ----
const ACTIVITY_LIMIT = 8
const activities = ref<ActivityItem[]>([])
const activitiesLoading = ref(true)
const activitiesError = ref(false)

// 动态与资源统计相互独立：动态接口慢或失败不应拖住整个概览
const loadActivities = async () => {
    activitiesLoading.value = true
    activitiesError.value = false
    try {
        const res = await activitiesApi.list({ limit: ACTIVITY_LIMIT })
        activities.value = res.activities || []
    } catch (err) {
        console.warn('Activities fetch failed:', err)
        activitiesError.value = true
    } finally {
        activitiesLoading.value = false
    }
}
onMounted(loadActivities)

const activityIcons: Record<string, Component> = {
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
const activityIconClass: Record<string, string> = {
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

// 资源类型 -> 详情页路由；没有详情页的类型（密钥、规格）只显示名称
const activityRoutes: Record<string, string> = {
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

// 文案里的 {name} 由模板插槽渲染为资源名（带链接）；后端新增了前端未翻译的动作时退回通用文案
// 失败的操作用单独的文案：成功文案是「删除了…」这类已完成的说法，接上「失败」会自相矛盾
const activityKey = (a: ActivityItem) => {
    const group = a.success ? 'activityActions' : 'activityActionsFailed'
    const key = `dashboard.overview.${group}.${a.action}`
    if (te(key)) return key
    return a.success ? 'dashboard.overview.activityActionUnknown' : 'dashboard.overview.activityActionUnknownFailed'
}

const activityResourceLabel = (a: ActivityItem) => a.resource_name || (a.resource_id ? a.resource_id.slice(0, 8) : '')

// 已删除或失败的操作，资源详情页大概率不存在，不给链接
const activityLink = (a: ActivityItem) => {
    if (!a.success || a.action.endsWith('.delete')) return null
    if (a.resource_type === 'zone' && a.resource_name) {
        return { name: 'zone-detail', params: { name: a.resource_name } }
    }
    const routeName = activityRoutes[a.resource_type]
    if (!routeName || !a.resource_id) return null
    return { name: routeName, params: { id: a.resource_id } }
}

const relativeTime = (iso: string) => {
    const diff = Date.now() - new Date(iso).getTime()
    const mins = Math.floor(diff / 60000)
    if (mins < 1) return t('dashboard.overview.justNow')
    if (mins < 60) return t('dashboard.overview.minsAgo', { n: mins })
    const hours = Math.floor(mins / 60)
    if (hours < 24) return t('dashboard.overview.hoursAgo', { n: hours })
    return t('dashboard.overview.daysAgo', { n: Math.floor(hours / 24) })
}

const getPercentColor = (percent: number) => {
    if (percent > 90) return 'var(--error-color)'
    if (percent > 75) return 'var(--warning-color)'
    return 'var(--primary-color)'
}
</script>

<template>
  <div class="overview-page" v-if="stats">
    
    <!-- Welcome Section -->
    <div class="welcome-section">

      <p class="welcome-subtitle">
        <i18n-t keypath="dashboard.overview.welcomeBack" tag="span">
          <template #username>
            <strong class="welcome-username">{{ displayName }}</strong>
          </template>
        </i18n-t>
      </p>
    </div>

    <!-- Summary Cards -->
    <div class="summary-grid">
      <!-- Instances -->
      <div class="stat-card">
        <div class="stat-icon-wrapper bg-blue-light">
          <Server class="stat-icon text-blue" :size="24" />
        </div>
        <div class="stat-content">
          <span class="stat-label">{{ $t('dashboard.instances') }}</span>
          <div class="stat-value-group">
            <span class="stat-value">{{ stats.instances.used }}</span>
            <span class="stat-total">{{ $t('dashboard.overview.activeCount') }}</span>
          </div>
        </div>
      </div>

      <!-- Volumes -->
      <div class="stat-card">
        <div class="stat-icon-wrapper bg-teal-light">
          <HardDrive class="stat-icon text-teal" :size="24" />
        </div>
        <div class="stat-content">
          <span class="stat-label">{{ $t('dashboard.volumes') }}</span>
          <div class="stat-value-group">
            <span class="stat-value">{{ stats.volume.used }}</span>
            <span class="stat-unit">{{ stats.volume.unit }}</span>
          </div>
        </div>
      </div>

      <!-- Floating IPs (Public IP) -->
      <div class="stat-card">
        <div class="stat-icon-wrapper bg-blue-light">
          <Globe class="stat-icon text-blue" :size="24" />
        </div>
        <div class="stat-content">
          <span class="stat-label">{{ $t('dashboard.floatingIPs') }}</span>
          <div class="stat-value-group">
            <span class="stat-value">{{ stats.public_ip.used }}</span>
            <span class="stat-total">{{ $t('dashboard.overview.activeCount') }}</span>
          </div>
        </div>
      </div>

      <!-- VPCs -->
      <div class="stat-card">
        <div class="stat-icon-wrapper bg-purple-light">
          <Layers class="stat-icon text-purple" :size="24" />
        </div>
        <div class="stat-content">
          <span class="stat-label">{{ $t('dashboard.vpcs') }}</span>
          <div class="stat-value-group">
            <span class="stat-value">{{ stats.private_ip.used }}</span>
            <span class="stat-total">{{ $t('dashboard.overview.activeCount') }}</span>
          </div>
        </div>
      </div>

      <!-- Images -->
      <div class="stat-card">
        <div class="stat-icon-wrapper bg-rose-light">
          <Disc class="stat-icon text-rose" :size="24" />
        </div>
        <div class="stat-content">
          <span class="stat-label">{{ $t('dashboard.images') }}</span>
          <div class="stat-value-group">
            <span class="stat-value">{{ stats.images.used }}</span>
            <span class="stat-total">{{ $t('dashboard.overview.imageCount') }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Main Content Grid -->
    <div class="dashboard-grid">
      
      <!-- Resource Usage -->
      <div class="card resource-card">
        <div class="card-header">
          <h3>{{ $t('dashboard.overview.resourceUsage') }}</h3>
        </div>
        <div class="card-body">
          <!-- CPU -->
          <div class="usage-item">
            <div class="usage-header">
              <span class="usage-label">{{ $t('dashboard.overview.cpu') }} {{ $t('dashboard.overview.usage') }}</span>
              <span class="usage-value">{{ stats.cpu.percentage }}%</span>
            </div>
            <div class="progress-bg">
              <div class="progress-fill" :style="{ width: stats.cpu.percentage + '%', backgroundColor: getPercentColor(stats.cpu.percentage) }"></div>
            </div>
            <div class="usage-meta">{{ stats.cpu.used }} / {{ stats.cpu.total }} {{ $t('dashboard.overview.cpuUnit') }}</div>
          </div>

          <!-- Memory -->
          <div class="usage-item">
            <div class="usage-header">
              <span class="usage-label">{{ $t('dashboard.overview.memory') }} {{ $t('dashboard.overview.usage') }}</span>
              <span class="usage-value">{{ stats.memory.percentage }}%</span>
            </div>
            <div class="progress-bg">
              <div class="progress-fill" :style="{ width: stats.memory.percentage + '%', backgroundColor: getPercentColor(stats.memory.percentage) }"></div>
            </div>
            <div class="usage-meta">{{ stats.memory.used }} / {{ stats.memory.total }} {{ stats.memory.unit }}</div>
          </div>

          <!-- Storage -->
          <div class="usage-item">
            <div class="usage-header">
              <span class="usage-label">{{ $t('dashboard.overview.disk') }} {{ $t('dashboard.overview.usage') }}</span>
              <span class="usage-value">{{ stats.disk.percentage }}%</span>
            </div>
            <div class="progress-bg">
              <div class="progress-fill" :style="{ width: stats.disk.percentage + '%', backgroundColor: getPercentColor(stats.disk.percentage) }"></div>
            </div>
            <div class="usage-meta">{{ stats.disk.used }} / {{ stats.disk.total }} {{ stats.disk.unit }}</div>
          </div>
        </div>
      </div>

      <!-- Recent Activity：当前组织在当前区域的操作记录 -->
      <div class="card activity-card">
        <div class="card-header">
          <h3>{{ $t('dashboard.overview.recentActivity') }}</h3>
        </div>
        <div class="card-body">
          <div v-if="activitiesLoading" class="activity-empty">{{ $t('dashboard.overview.activityLoading') }}</div>
          <div v-else-if="activitiesError" class="activity-empty">
            {{ $t('dashboard.overview.activityLoadFailed') }}
            <button class="activity-retry" @click="loadActivities">{{ $t('dashboard.overview.activityRetry') }}</button>
          </div>
          <div v-else-if="activities.length === 0" class="activity-empty">{{ $t('dashboard.overview.activityEmpty') }}</div>
          <ul v-else class="activity-list">
            <li v-for="a in activities" :key="a.id" class="activity-item">
              <div class="activity-icon" :class="activityIconClass[a.resource_type] || 'bg-gray-light text-gray'">
                <component :is="activityIcons[a.resource_type] || Activity" :size="16" />
              </div>
              <div class="activity-details">
                <span class="activity-text">
                  <strong>{{ a.actor || $t('dashboard.overview.activityUnknownActor') }}</strong>
                  {{ ' ' }}
                  <i18n-t :keypath="activityKey(a)" tag="span">
                    <template #action>{{ a.action }}</template>
                    <template #name>
                      <router-link v-if="activityLink(a)" :to="activityLink(a)!" class="activity-resource" :title="activityResourceLabel(a)">{{ activityResourceLabel(a) }}</router-link>
                      <span v-else class="activity-resource plain" :title="activityResourceLabel(a)">{{ activityResourceLabel(a) }}</span>
                    </template>
                  </i18n-t>
                  <span v-if="!a.success" class="activity-failed">{{ $t('dashboard.overview.activityFailed') }}</span>
                </span>
                <span class="activity-time" :title="new Date(a.created_at).toLocaleString()">{{ relativeTime(a.created_at) }}</span>
              </div>
            </li>
          </ul>
        </div>
      </div>
    </div>
  </div>
  <div v-else class="loading-container">
    <div class="loading-spinner"></div>
    <p>{{ $t('dashboard.overview.loadingOverview') }}</p>
  </div>
</template>

<style scoped>
.overview-page {
  max-width: 1400px;
  margin: 0 auto;
  padding: 0;
}

.welcome-section {
  margin-bottom: 32px;
  padding-left: 15px;
}

.welcome-title {
  font-size: 1.75rem;
  font-weight: 700;
  color: var(--gray-800);
  margin-bottom: 8px;
  letter-spacing: -0.02em;
}

.welcome-subtitle {
  color: var(--gray-500);
  font-size: 1rem;
}

.welcome-username {
  color: var(--gray-800);
  font-weight: 700;
}

/* Summary Grid */
.summary-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 24px;
  margin-bottom: 32px;
}

.stat-card {
  background: var(--bg-primary);
  padding: 24px;
  border-radius: var(--radius-lg); /* 16px */
  box-shadow: var(--shadow-card);
  display: flex;
  align-items: center;
  gap: 20px;
  transition: transform 0.2s, box-shadow 0.2s;
  border: 1px solid var(--border-light);
}

.stat-card:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-card-hover);
}

.stat-icon-wrapper {
  width: 56px;
  height: 56px;
  border-radius: var(--radius-xl); /* 24px */
  display: flex;
  align-items: center;
  justify-content: center;
}

/* Colors helpers */
.bg-blue-light { background-color: var(--primary-100); }
.text-blue { color: var(--primary-600); }

.bg-teal-light { background-color: var(--accent-teal-light); }
.text-teal { color: var(--accent-teal); }

.bg-purple-light { background-color: var(--accent-purple-light); }
.text-purple { color: var(--accent-purple); }

.bg-rose-light { background-color: var(--accent-rose-light); }
.text-rose { color: var(--accent-rose); }


.stat-content {
  display: flex;
  flex-direction: column;
}

.stat-label {
  font-size: 0.875rem;
  font-weight: 500;
  color: var(--gray-500);
  margin-bottom: 4px;
}

.stat-value-group {
  display: flex;
  align-items: baseline;
  gap: 4px;
}

.stat-value {
  font-size: 1.75rem;
  font-weight: 700;
  color: var(--gray-900);
  line-height: 1;
}

.stat-total, .stat-unit {
  font-size: 0.875rem;
  color: var(--gray-400);
  font-weight: 500;
}

/* Dashboard Grid */
.dashboard-grid {
  display: grid;
  grid-template-columns: 1.5fr 1fr;
  gap: 24px;
}

@media (max-width: 1024px) {
  .dashboard-grid {
    grid-template-columns: 1fr;
  }
}

.card {
  background: var(--bg-primary);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-card);
  border: 1px solid var(--border-light);
  overflow: hidden;
}

.card-header {
  padding: 24px;
  border-bottom: 1px solid var(--border-light);
}

.card-header h3 {
  margin: 0;
  font-size: 1.125rem;
  font-weight: 600;
  color: var(--gray-800);
}

.card-body {
  padding: 24px;
}

/* Resource Usage */
.usage-item {
  margin-bottom: 24px;
}

.usage-item:last-child {
  margin-bottom: 0;
}

.usage-header {
  display: flex;
  justify-content: space-between;
  margin-bottom: 8px;
}

.usage-label {
  font-size: 0.9375rem;
  font-weight: 500;
  color: var(--gray-700);
}

.usage-value {
  font-weight: 600;
  color: var(--gray-900);
}

.progress-bg {
  height: 10px;
  background-color: var(--gray-100);
  border-radius: 999px;
  overflow: hidden;
  margin-bottom: 8px;
}

.progress-fill {
  height: 100%;
  border-radius: 999px;
  transition: width 0.6s cubic-bezier(0.4, 0, 0.2, 1);
}

.usage-meta {
  text-align: right;
  font-size: 0.8125rem;
  color: var(--gray-500);
}

/* Activity List */
.activity-list {
  list-style: none;
  padding: 0;
  margin: 0;
}

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

.activity-details {
  display: flex;
  flex-direction: column;
}

.activity-text {
  font-size: 0.9375rem;
  color: var(--gray-700);
  margin-bottom: 2px;
}

.activity-text strong {
  color: var(--gray-900);
  font-weight: 600;
}

.activity-time {
  font-size: 0.75rem;
  color: var(--gray-400);
}

.bg-gray-light { background-color: var(--gray-100); }
.text-gray { color: var(--gray-500); }

.activity-details {
  min-width: 0;
}

.activity-text {
  overflow-wrap: anywhere;
}

/* 资源名可能很长（名称上限由各资源自行决定，审计列最长 255 字符），单行截断，悬停看全名 */
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

.activity-empty {
  padding: 24px 0;
  text-align: center;
  font-size: 0.875rem;
  color: var(--gray-400);
}

.activity-retry {
  margin-left: 8px;
  padding: 0;
  border: none;
  background: none;
  color: var(--primary-600);
  cursor: pointer;
  font-size: inherit;
}

.loading-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 400px;
  color: var(--gray-400);
}

.loading-spinner {
  width: 40px;
  height: 40px;
  border: 3px solid var(--gray-200);
  border-top-color: var(--primary-color);
  border-radius: 50%;
  animation: spin 1s linear infinite;
  margin-bottom: 16px;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}
</style>

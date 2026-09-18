<script setup lang="ts">
import { ref, computed, onMounted, type Component } from 'vue'
import { useAuthStore } from '../../stores/auth'
import { useRegionStore } from '../../stores/region'
import { Server, HardDrive, Layers, Disc, Globe, Network, ChevronRight } from 'lucide-vue-next'

import { instancesApi, type Instance } from '../../api/instances'
import { activitiesApi, type Activity as ActivityItem } from '../../api/activities'
import ActivityEntry from '../../components/activity/ActivityEntry.vue'
import { volumesApi } from '../../api/volumes'
import { imagesApi } from '../../api/images'
import { vpcsApi, floatingIpsApi, loadBalancersApi } from '../../api/networks'
import { quotaApi, type QuotaFields } from '../../api/quota'
import { useToast } from '../../composables/useToast'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()
const toast = useToast()
const auth = useAuthStore()
const regionStore = useRegionStore()
const displayName = computed(() => auth.user?.username || auth.user?.name || 'User')

interface ResourceUsage {
    used: number
    total: number
    unit?: string
    percentage: number
}

// Figures for the summary cards; quota usage is shown by usageBars
interface SystemStats {
    instances: ResourceUsage
    volume: ResourceUsage
    images: ResourceUsage
    public_ip: ResourceUsage
    load_balancer: ResourceUsage
    private_ip: ResourceUsage
}

const loading = ref(true)
const stats = ref<SystemStats | null>(null)

// One progress bar per quota: usage against the org quota in the current region (not live utilization)
interface UsageBar {
    key: string
    labelKey: string
    used: number
    // null when no quota data is available; 0 is a real quota (the resource is disabled)
    total: number | null
    percentage: number
    // Translated unit (unitKey) or a literal unit such as GB
    unitKey?: string
    unit?: string
}
const usageBars = ref<UsageBar[]>([])

const usagePercent = (used: number, total: number | null) => {
    if (total === null) return 0
    if (total <= 0) return used > 0 ? 100 : 0
    return Math.min(Math.round((used / total) * 100), 100)
}

const buildUsageBars = (u: {
    cpu: number, memGB: number, diskGB: number, publicIps: number, vpcs: number, loadBalancers: number, images: number
}, quota?: Partial<QuotaFields> | null): UsageBar[] => {
    const bar = (key: string, labelKey: string, used: number, total: number | undefined, unit: { unitKey?: string, unit?: string }): UsageBar => {
        const limit = total ?? null
        return { key, labelKey, used, total: limit, percentage: usagePercent(used, limit), ...unit }
    }
    const count = { unitKey: 'dashboard.overview.countUnit' }
    return [
        bar('cpu', 'dashboard.overview.cpu', u.cpu, quota?.max_cpu_cores, { unitKey: 'dashboard.overview.cpuUnit' }),
        bar('memory', 'dashboard.overview.memory', Math.round(u.memGB), quota?.max_ram_gb, { unit: 'GB' }),
        bar('disk', 'dashboard.overview.diskTotal', u.diskGB, quota?.max_disk_gb, { unit: 'GB' }),
        bar('public_ips', 'dashboard.overview.publicIp', u.publicIps, quota?.max_public_ips, count),
        bar('vpcs', 'dashboard.overview.vpc', u.vpcs, quota?.max_vpcs, count),
        bar('load_balancers', 'dashboard.overview.loadBalancer', u.loadBalancers, quota?.max_load_balancers, count),
        bar('images', 'dashboard.overview.images', u.images, quota?.max_images, count),
    ]
}

// Default empty data pattern (as fallback)
const emptyStats: SystemStats = {
    instances: { used: 0, total: 0, percentage: 0 },
    volume: { used: 0, total: 0, unit: 'GB', percentage: 0 },
    images: { used: 0, total: 0, percentage: 0 },
    public_ip: { used: 0, total: 0, percentage: 0 },
    load_balancer: { used: 0, total: 0, percentage: 0 },
    private_ip: { used: 0, total: 0, percentage: 0 }
}

onMounted(async () => {
    try {
        // Fetch resource data and quota in parallel
        const orgUuid = auth.user?.current_org_uuid || ''
        const regionUuid = regionStore.currentRegionId || ''

        const [instRes, volRes, imgRes, vpcRes, fipRes, lbRes, quotaRes] = await Promise.all([
            instancesApi.fetchInstances().catch(err => { console.warn('Instances fetch failed:', err); return { data: [] } }),
            volumesApi.list({ limit: 100, type: 'all' }).catch(err => { console.warn('Volumes fetch failed:', err); return { volumes: [] } }),
            imagesApi.fetchImages().catch(err => { console.warn('Images fetch failed:', err); return { data: [] } }),
            vpcsApi.list({ limit: 100 }).catch(err => { console.warn('VPCs fetch failed:', err); return { vpcs: [] } }),
            floatingIpsApi.list({ limit: 100 }).catch(err => { console.warn('FIPs fetch failed:', err); return { floating_ips: [] } }),
            // Only the total is needed for the load balancer card
            loadBalancersApi.list({ limit: 1 }).catch(err => { console.warn('Load balancers fetch failed:', err); return null }),
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

        const instanceCount = Array.isArray(instances) ? instances.length : 0
        const imageCount = Array.isArray(images) ? images.length : 0
        const vpcCount = Array.isArray(vpcs) ? vpcs.length : 0

        stats.value = {
            instances: { used: instanceCount, total: 0, percentage: 0 },
            volume: { used: usedDiskGB, total: 0, unit: 'GB', percentage: 0 },
            images: { used: imageCount, total: 0, percentage: 0 },
            public_ip: { used: usedPublicIps, total: 0, percentage: 0 },
            load_balancer: { used: consumption?.load_balancers ?? lbRes?.total ?? 0, total: 0, percentage: 0 },
            private_ip: { used: vpcCount, total: 0, percentage: 0 }
        }
        // Without quota data the limits stay null and each bar shows "used / -"
        usageBars.value = buildUsageBars({
            cpu: usedCpu, memGB: usedMemGB, diskGB: usedDiskGB, publicIps: usedPublicIps,
            vpcs: consumption?.vpcs ?? vpcCount, loadBalancers: consumption?.load_balancers ?? lbRes?.total ?? 0, images: consumption?.images ?? 0,
        }, quota)
    } catch (error) {
        // 静默清零会让用户把"加载失败"看成"用量真的是 0"，必须明确提示
        console.error('Failed to fetch actual stats:', error)
        toast.error(t('messages.error'))
        stats.value = emptyStats
        usageBars.value = buildUsageBars({ cpu: 0, memGB: 0, diskGB: 0, publicIps: 0, vpcs: 0, loadBalancers: 0, images: 0 }, null)
    } finally {
        loading.value = false
    }
})

// Summary cards; each links to the list page of its resource
interface SummaryCard {
    key: string
    route: string
    labelKey: string
    icon: Component
    bgClass: string
    textClass: string
    value: number
    suffixKey?: string
    suffix?: string
}
const summaryCards = computed<SummaryCard[]>(() => {
    const s = stats.value
    if (!s) return []
    return [
        { key: 'instances', route: 'instances', labelKey: 'dashboard.instances', icon: Server, bgClass: 'bg-blue-light', textClass: 'text-blue', value: s.instances.used, suffixKey: 'dashboard.overview.activeCount' },
        { key: 'volumes', route: 'volumes', labelKey: 'dashboard.volumes', icon: HardDrive, bgClass: 'bg-teal-light', textClass: 'text-teal', value: s.volume.used, suffix: s.volume.unit },
        { key: 'floating-ips', route: 'floating-ips', labelKey: 'dashboard.floatingIPs', icon: Globe, bgClass: 'bg-blue-light', textClass: 'text-blue', value: s.public_ip.used, suffixKey: 'dashboard.overview.activeCount' },
        { key: 'vpcs', route: 'vpcs', labelKey: 'dashboard.vpcs', icon: Layers, bgClass: 'bg-purple-light', textClass: 'text-purple', value: s.private_ip.used, suffixKey: 'dashboard.overview.activeCount' },
        { key: 'load-balancers', route: 'load-balancers', labelKey: 'dashboard.loadBalancers', icon: Network, bgClass: 'bg-teal-light', textClass: 'text-teal', value: s.load_balancer.used, suffixKey: 'dashboard.overview.activeCount' },
        { key: 'images', route: 'images', labelKey: 'dashboard.images', icon: Disc, bgClass: 'bg-rose-light', textClass: 'text-rose', value: s.images.used, suffixKey: 'dashboard.overview.imageCount' },
    ]
})

// ---- Recent activity: operations of the current organization in the current region ----
const ACTIVITY_LIMIT = 8
const activities = ref<ActivityItem[]>([])
const activitiesLoading = ref(true)
const activitiesError = ref(false)

// Loaded independently of the resource stats: a slow or failing activity API must not block the overview.
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

// Over quota (a lowered limit, a quota of 0, or usage recorded before enforcement) is always red
const getBarColor = (bar: UsageBar) => bar.total !== null && bar.used > bar.total ? 'var(--error-color)' : getPercentColor(bar.percentage)

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

    <!-- Summary Cards: each links to the resource's list page -->
    <div class="summary-grid">
      <router-link v-for="card in summaryCards" :key="card.key" :to="{ name: card.route }" class="stat-card">
        <div class="stat-icon-wrapper" :class="card.bgClass">
          <component :is="card.icon" class="stat-icon" :class="card.textClass" :size="24" />
        </div>
        <div class="stat-content">
          <span class="stat-label">{{ $t(card.labelKey) }}</span>
          <div class="stat-value-group">
            <span class="stat-value">{{ card.value }}</span>
            <span class="stat-total">{{ card.suffixKey ? $t(card.suffixKey) : card.suffix }}</span>
          </div>
        </div>
      </router-link>
    </div>

    <!-- Main Content Grid -->
    <div class="dashboard-grid">
      
      <!-- Resource Usage -->
      <div class="card resource-card">
        <div class="card-header">
          <h3>{{ $t('dashboard.overview.resourceUsage') }}</h3>
        </div>
        <div class="card-body">
          <div v-for="bar in usageBars" :key="bar.key" class="usage-item">
            <div class="usage-header">
              <span class="usage-label">{{ $t(bar.labelKey) }} {{ $t('dashboard.overview.usage') }}</span>
              <span class="usage-value">{{ bar.total !== null ? bar.percentage + '%' : '-' }}</span>
            </div>
            <div class="progress-bg">
              <div class="progress-fill" :style="{ width: bar.percentage + '%', backgroundColor: getBarColor(bar) }"></div>
            </div>
            <div class="usage-meta">{{ bar.used }} / {{ bar.total !== null ? bar.total : '-' }} {{ bar.unitKey ? $t(bar.unitKey) : bar.unit }}</div>
          </div>
        </div>
      </div>

      <!-- Recent Activity: operations of the current organization in the current region -->
      <div class="card activity-card">
        <div class="card-header card-header-with-action">
          <h3>{{ $t('dashboard.overview.recentActivity') }}</h3>
          <router-link :to="{ name: 'activities' }" class="card-header-link">
            {{ $t('dashboard.overview.activityViewAll') }}
            <ChevronRight :size="14" />
          </router-link>
        </div>
        <div class="card-body">
          <div v-if="activitiesLoading" class="activity-empty">{{ $t('dashboard.overview.activityLoading') }}</div>
          <div v-else-if="activitiesError" class="activity-empty">
            {{ $t('dashboard.overview.activityLoadFailed') }}
            <button class="activity-retry" @click="loadActivities">{{ $t('dashboard.overview.activityRetry') }}</button>
          </div>
          <div v-else-if="activities.length === 0" class="activity-empty">{{ $t('dashboard.overview.activityEmpty') }}</div>
          <ul v-else class="activity-list">
            <ActivityEntry v-for="a in activities" :key="a.id" :activity="a" />
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
  /* six cards fit on one row on a typical desktop width */
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
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

/* Cards are links to the resource list pages */
a.stat-card {
  color: inherit;
  text-decoration: none;
  cursor: pointer;
}

a.stat-card:focus-visible {
  outline: 2px solid var(--primary-color);
  outline-offset: 2px;
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

.card-header-with-action {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.card-header-link {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  font-size: 0.875rem;
  font-weight: 500;
  color: var(--primary-600);
  text-decoration: none;
  white-space: nowrap;
}

.card-header-link:hover {
  text-decoration: underline;
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

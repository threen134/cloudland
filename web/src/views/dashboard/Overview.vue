<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useAuthStore } from '../../stores/auth'
import { useRegionStore } from '../../stores/region'
import { useI18n } from 'vue-i18n'
import { Server, HardDrive, Cpu, Layers, Disc, GitFork, Activity, Globe } from 'lucide-vue-next'

import { instancesApi, type Instance } from '../../api/instances'
import { volumesApi } from '../../api/volumes'
import { imagesApi, type Image } from '../../api/images'
import { vpcsApi, floatingIpsApi } from '../../api/networks'
import { quotaApi } from '../../api/quota'

const auth = useAuthStore()
const regionStore = useRegionStore()
const { t } = useI18n()
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
            volumesApi.list({ limit: 100 }).catch(err => { console.warn('Volumes fetch failed:', err); return { volumes: [] } }),
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

      <!-- Recent Activity (Mock) -->
      <div class="card activity-card">
        <div class="card-header">
          <h3>{{ $t('dashboard.overview.recentActivity') }}</h3>
        </div>
        <div class="card-body">
          <ul class="activity-list">
            <li class="activity-item">
              <div class="activity-icon bg-blue-light"><Server :size="16" class="text-blue"/></div>
              <div class="activity-details">
                <span class="activity-text">{{ $t('dashboard.overview.activity.instanceStarted', { name: 'web-server-01' }) }}</span>
                <span class="activity-time">{{ $t('dashboard.overview.minsAgo', { n: 10 }) }}</span>
              </div>
            </li>
             <li class="activity-item">
              <div class="activity-icon bg-teal-light"><HardDrive :size="16" class="text-teal"/></div>
              <div class="activity-details">
                <span class="activity-text">{{ $t('dashboard.overview.activity.volumeAttached', { name: 'data-vol-01' }) }}</span>
                <span class="activity-time">{{ $t('dashboard.overview.hoursAgo', { n: 1 }) }}</span>
              </div>
            </li>
             <li class="activity-item">
              <div class="activity-icon bg-purple-light"><Layers :size="16" class="text-purple"/></div>
              <div class="activity-details">
                <span class="activity-text">{{ $t('dashboard.overview.activity.vpcCreated', { name: 'dev-env' }) }}</span>
                <span class="activity-time">{{ $t('dashboard.overview.hoursAgo', { n: 3 }) }}</span>
              </div>
            </li>
             <li class="activity-item">
              <div class="activity-icon bg-rose-light"><Activity :size="16" class="text-rose"/></div>
              <div class="activity-details">
                <span class="activity-text">{{ $t('dashboard.overview.activity.sgRuleUpdated') }}</span>
                <span class="activity-time">{{ $t('dashboard.overview.hoursAgo', { n: 5 }) }}</span>
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

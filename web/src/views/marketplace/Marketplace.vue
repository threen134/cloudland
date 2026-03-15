<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Search } from 'lucide-vue-next'

interface AppItem {
    id: string
    nameKey: string
    descKey: string
    icon: string
    provider: string
    categories: string[]
    version?: string
    popular?: boolean
    isNew?: boolean
    free?: boolean
}

type FilterCategory = 'all' | 'database' | 'web' | 'container' | 'devops' | 'security' | 'panel' | 'messaging' | 'monitoring' | 'ai'

const { t } = useI18n()
const router = useRouter()

const searchQuery = ref('')
const activeFilter = ref<FilterCategory>('all')

const filterCategories: { key: FilterCategory; labelKey: string; count?: number }[] = [
    { key: 'all', labelKey: 'marketplace.filters.all' },
    { key: 'database', labelKey: 'marketplace.filters.database' },
    { key: 'web', labelKey: 'marketplace.filters.web' },
    { key: 'container', labelKey: 'marketplace.filters.container' },
    { key: 'devops', labelKey: 'marketplace.filters.devops' },
    { key: 'security', labelKey: 'marketplace.filters.security' },
    { key: 'panel', labelKey: 'marketplace.filters.panel' },
    { key: 'messaging', labelKey: 'marketplace.filters.messaging' },
    { key: 'monitoring', labelKey: 'marketplace.filters.monitoring' },
    { key: 'ai', labelKey: 'marketplace.filters.ai' },
]

// Application catalog — all apps
const apps: AppItem[] = [
    // Databases
    { id: 'mysql', nameKey: 'marketplace.apps.mysql', descKey: 'marketplace.apps.mysqlDesc', icon: '🐬', provider: 'Oracle', categories: ['database'], version: '8.0 / 5.7', popular: true, free: true },
    { id: 'redis', nameKey: 'marketplace.apps.redis', descKey: 'marketplace.apps.redisDesc', icon: '⚡', provider: 'Redis Ltd.', categories: ['database'], version: '7.2', popular: true, free: true },
    { id: 'mongodb', nameKey: 'marketplace.apps.mongodb', descKey: 'marketplace.apps.mongodbDesc', icon: '🍃', provider: 'MongoDB Inc.', categories: ['database'], version: '7.0', free: true },
    { id: 'postgresql', nameKey: 'marketplace.apps.postgresql', descKey: 'marketplace.apps.postgresqlDesc', icon: '🐘', provider: 'PostgreSQL', categories: ['database'], version: '16', popular: true, free: true },
    { id: 'mariadb', nameKey: 'marketplace.apps.mariadb', descKey: 'marketplace.apps.mariadbDesc', icon: '🦭', provider: 'MariaDB Foundation', categories: ['database'], version: '11.2', free: true },
    { id: 'elasticsearch', nameKey: 'marketplace.apps.elasticsearch', descKey: 'marketplace.apps.elasticsearchDesc', icon: '🔎', provider: 'Elastic', categories: ['database', 'monitoring'], version: '8.12', free: true },
    { id: 'clickhouse', nameKey: 'marketplace.apps.clickhouse', descKey: 'marketplace.apps.clickhouseDesc', icon: '🏠', provider: 'ClickHouse Inc.', categories: ['database'], version: '24.1', isNew: true, free: true },

    // Web Servers
    { id: 'nginx', nameKey: 'marketplace.apps.nginx', descKey: 'marketplace.apps.nginxDesc', icon: '🌐', provider: 'F5 / Nginx Inc.', categories: ['web'], version: '1.25', popular: true, free: true },
    { id: 'apache', nameKey: 'marketplace.apps.apache', descKey: 'marketplace.apps.apacheDesc', icon: '🪶', provider: 'Apache Foundation', categories: ['web'], version: '2.4', free: true },
    { id: 'caddy', nameKey: 'marketplace.apps.caddy', descKey: 'marketplace.apps.caddyDesc', icon: '🔒', provider: 'Caddy', categories: ['web', 'security'], version: '2.7', isNew: true, free: true },
    { id: 'tomcat', nameKey: 'marketplace.apps.tomcat', descKey: 'marketplace.apps.tomcatDesc', icon: '🐱', provider: 'Apache Foundation', categories: ['web'], version: '10.1', free: true },
    { id: 'openlitespeed', nameKey: 'marketplace.apps.openlitespeed', descKey: 'marketplace.apps.openlitespeedDesc', icon: '⚡', provider: 'LiteSpeed', categories: ['web'], version: '1.7', free: true },

    // Containers & DevOps
    { id: 'docker', nameKey: 'marketplace.apps.docker', descKey: 'marketplace.apps.dockerDesc', icon: '🐳', provider: 'Docker Inc.', categories: ['container', 'devops'], version: 'CE 25.0', popular: true, free: true },
    { id: 'kubernetes', nameKey: 'marketplace.apps.kubernetes', descKey: 'marketplace.apps.kubernetesDesc', icon: '☸️', provider: 'CNCF', categories: ['container', 'devops'], version: '1.29', popular: true, free: true },
    { id: 'portainer', nameKey: 'marketplace.apps.portainer', descKey: 'marketplace.apps.portainerDesc', icon: '📦', provider: 'Portainer.io', categories: ['container', 'panel'], version: 'CE 2.19', free: true },
    { id: 'jenkins', nameKey: 'marketplace.apps.jenkins', descKey: 'marketplace.apps.jenkinsDesc', icon: '🔧', provider: 'Jenkins Project', categories: ['devops'], version: '2.440', free: true },
    { id: 'gitlab', nameKey: 'marketplace.apps.gitlab', descKey: 'marketplace.apps.gitlabDesc', icon: '🦊', provider: 'GitLab Inc.', categories: ['devops'], version: 'CE 16.8', free: true },
    { id: 'ansible', nameKey: 'marketplace.apps.ansible', descKey: 'marketplace.apps.ansibleDesc', icon: '🤖', provider: 'Red Hat', categories: ['devops'], version: '2.16', free: true },
    { id: 'terraform', nameKey: 'marketplace.apps.terraform', descKey: 'marketplace.apps.terraformDesc', icon: '🏗️', provider: 'HashiCorp', categories: ['devops'], version: '1.7', free: true },

    // Security & Network
    { id: 'ipsec', nameKey: 'marketplace.apps.ipsec', descKey: 'marketplace.apps.ipsecDesc', icon: '🔐', provider: 'IBM Cloud China', categories: ['security'], version: 'Latest', free: true },
    { id: 'openvpn', nameKey: 'marketplace.apps.openvpn', descKey: 'marketplace.apps.openvpnDesc', icon: '🛡️', provider: 'OpenVPN Inc.', categories: ['security'], version: '2.6', free: true },
    { id: 'wireguard', nameKey: 'marketplace.apps.wireguard', descKey: 'marketplace.apps.wireguardDesc', icon: '🔑', provider: 'WireGuard', categories: ['security'], version: '1.0', isNew: true, free: true },
    { id: 'certbot', nameKey: 'marketplace.apps.certbot', descKey: 'marketplace.apps.certbotDesc', icon: '📜', provider: "Let's Encrypt", categories: ['security', 'web'], version: '2.8', free: true },

    // Control Panels
    { id: 'bt', nameKey: 'marketplace.apps.bt', descKey: 'marketplace.apps.btDesc', icon: '🐧', provider: 'Baota', categories: ['panel'], version: 'Linux 8.x', popular: true, free: true },
    { id: 'onepanel', nameKey: 'marketplace.apps.onepanel', descKey: 'marketplace.apps.onepanelDesc', icon: '🎛️', provider: '1Panel', categories: ['panel'], version: '1.9', isNew: true, free: true },
    { id: 'webmin', nameKey: 'marketplace.apps.webmin', descKey: 'marketplace.apps.webminDesc', icon: '🖥️', provider: 'Webmin', categories: ['panel'], version: '2.105', free: true },

    // Messaging & Queue
    { id: 'rabbitmq', nameKey: 'marketplace.apps.rabbitmq', descKey: 'marketplace.apps.rabbitmqDesc', icon: '🐰', provider: 'VMware', categories: ['messaging'], version: '3.13', free: true },
    { id: 'kafka', nameKey: 'marketplace.apps.kafka', descKey: 'marketplace.apps.kafkaDesc', icon: '📡', provider: 'Apache Foundation', categories: ['messaging'], version: '3.6', popular: true, free: true },
    { id: 'rocketmq', nameKey: 'marketplace.apps.rocketmq', descKey: 'marketplace.apps.rocketmqDesc', icon: '🚀', provider: 'Apache Foundation', categories: ['messaging'], version: '5.1', free: true },

    // Monitoring
    { id: 'prometheus', nameKey: 'marketplace.apps.prometheus', descKey: 'marketplace.apps.prometheusDesc', icon: '🔥', provider: 'CNCF', categories: ['monitoring'], version: '2.49', free: true },
    { id: 'grafana', nameKey: 'marketplace.apps.grafana', descKey: 'marketplace.apps.grafanaDesc', icon: '📊', provider: 'Grafana Labs', categories: ['monitoring'], version: '10.3', popular: true, free: true },
    { id: 'zabbix', nameKey: 'marketplace.apps.zabbix', descKey: 'marketplace.apps.zabbixDesc', icon: '📈', provider: 'Zabbix LLC', categories: ['monitoring'], version: '6.4', free: true },

    // AI / ML
    { id: 'ollama', nameKey: 'marketplace.apps.ollama', descKey: 'marketplace.apps.ollamaDesc', icon: '🦙', provider: 'Ollama', categories: ['ai'], version: '0.1', isNew: true, free: true },
    { id: 'jupyterlab', nameKey: 'marketplace.apps.jupyterlab', descKey: 'marketplace.apps.jupyterlabDesc', icon: '📓', provider: 'Project Jupyter', categories: ['ai', 'devops'], version: '4.1', free: true },
    { id: 'tensorflow', nameKey: 'marketplace.apps.tensorflow', descKey: 'marketplace.apps.tensorflowDesc', icon: '🧠', provider: 'Google', categories: ['ai'], version: '2.15', free: true },
]

// Compute category counts
const categoryCounts = computed(() => {
    const counts: Record<string, number> = { all: apps.length }
    for (const cat of filterCategories) {
        if (cat.key !== 'all') {
            counts[cat.key] = apps.filter(a => a.categories.includes(cat.key)).length
        }
    }
    return counts
})

// Filtered apps
const filteredApps = computed(() => {
    let result = apps
    if (activeFilter.value !== 'all') {
        result = result.filter(a => a.categories.includes(activeFilter.value))
    }
    if (searchQuery.value.trim()) {
        const q = searchQuery.value.trim().toLowerCase()
        result = result.filter(a => {
            const name = t(a.nameKey).toLowerCase()
            const desc = t(a.descKey).toLowerCase()
            const provider = a.provider.toLowerCase()
            return name.includes(q) || desc.includes(q) || provider.includes(q) || a.id.includes(q)
        })
    }
    return result
})

const getCategoryLabel = (cat: string): string => {
    const key = `marketplace.filters.${cat}` as string
    try { return t(key) } catch { return cat }
}

const handleDeploy = (app: AppItem) => {
    router.push({ name: 'instances', query: { app: app.id } })
}
</script>

<template>
  <div class="catalog-page">
    <!-- Catalog Header -->
    <section class="catalog-header">
      <div class="catalog-header-content">
        <h1 class="catalog-title">{{ t('marketplace.title') }}</h1>
        <p class="catalog-subtitle">{{ t('marketplace.subtitle') }}</p>
        <div class="catalog-search-wrapper">
          <Search :size="20" class="search-icon" />
          <input
            v-model="searchQuery"
            type="text"
            class="catalog-search-input"
            :placeholder="t('marketplace.searchPlaceholder')"
          />
        </div>
      </div>
    </section>

    <!-- Filter Tabs -->
    <section class="catalog-filters">
      <div class="filter-tabs-wrapper">
        <div class="filter-tabs">
          <button
            v-for="cat in filterCategories"
            :key="cat.key"
            :class="['filter-tab', { active: activeFilter === cat.key }]"
            @click="activeFilter = cat.key"
          >
            {{ t(cat.labelKey) }}
            <span class="filter-count">{{ categoryCounts[cat.key] }}</span>
          </button>
        </div>
      </div>
    </section>

    <!-- Results Info -->
    <section class="catalog-body">
      <div class="catalog-container">
        <p class="results-info">
          {{ t('marketplace.viewingProducts', { count: filteredApps.length }) }}
        </p>

        <!-- Cards Grid -->
        <div class="catalog-grid">
          <div
            v-for="app in filteredApps"
            :key="app.id"
            class="catalog-card"
            @click="handleDeploy(app)"
          >
            <!-- Card Header -->
            <div class="card-top">
              <div class="card-icon">{{ app.icon }}</div>
              <div class="card-title-section">
                <h3 class="card-name">{{ t(app.nameKey) }}</h3>
                <span class="card-provider">{{ app.provider }}</span>
              </div>
            </div>

            <!-- Description -->
            <p class="card-desc">{{ t(app.descKey) }}</p>

            <!-- Version if present -->
            <div v-if="app.version" class="card-version">
              v{{ app.version }}
            </div>

            <!-- Tags -->
            <div class="card-tags">
              <span v-for="cat in app.categories" :key="cat" class="tag tag-category">
                {{ getCategoryLabel(cat) }}
              </span>
              <span v-if="app.free" class="tag tag-free">{{ t('marketplace.free') }}</span>
              <span v-if="app.popular" class="tag tag-popular">{{ t('pricing.popular') }}</span>
              <span v-if="app.isNew" class="tag tag-new">NEW</span>
              <span class="tag tag-soon">{{ t('marketplace.comingSoon') }}</span>
            </div>
          </div>
        </div>

        <!-- Empty State -->
        <div v-if="filteredApps.length === 0" class="empty-state">
          <div class="empty-icon">🔍</div>
          <h3>{{ t('messages.noData') }}</h3>
          <p>{{ t('marketplace.noResults') }}</p>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* ============================================
   IBM CLOUD CATALOG INSPIRED LAYOUT
   ============================================ */

.catalog-page {
    min-height: 100vh;
    background-color: var(--bg-secondary);
}

/* --- Catalog Header --- */
.catalog-header {
    background: var(--bg-primary);
    border-bottom: 1px solid var(--border-default);
    padding: var(--spacing-10) var(--spacing-8) var(--spacing-8);
}

.catalog-header-content {
    max-width: var(--max-width-xl);
    margin: 0 auto;
}

.catalog-title {
    font-size: var(--font-size-4xl);
    font-weight: var(--font-weight-bold);
    color: var(--text-primary);
    margin: 0 0 var(--spacing-2) 0;
    letter-spacing: -0.02em;
}

.catalog-subtitle {
    font-size: var(--font-size-base);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-6) 0;
}

.catalog-search-wrapper {
    position: relative;
    max-width: 560px;
}

.search-icon {
    position: absolute;
    left: var(--spacing-4);
    top: 50%;
    transform: translateY(-50%);
    color: var(--text-tertiary);
    pointer-events: none;
}

.catalog-search-input {
    width: 100%;
    padding: var(--spacing-3) var(--spacing-4) var(--spacing-3) 48px;
    font-size: var(--font-size-base);
    font-family: var(--font-family);
    color: var(--text-primary);
    background: var(--bg-secondary);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    outline: none;
    transition: border-color var(--transition-base), box-shadow var(--transition-base);
}

.catalog-search-input::placeholder {
    color: var(--text-tertiary);
}

.catalog-search-input:focus {
    border-color: var(--primary-color);
    box-shadow: 0 0 0 3px rgba(14, 165, 233, 0.1);
}

/* --- Filter Tabs --- */
.catalog-filters {
    background: var(--bg-primary);
    border-bottom: 1px solid var(--border-default);
    position: sticky;
    top: 0;
    z-index: var(--z-sticky);
}

.filter-tabs-wrapper {
    max-width: var(--max-width-xl);
    margin: 0 auto;
    padding: 0 var(--spacing-8);
    overflow-x: auto;
    scrollbar-width: none;
}

.filter-tabs-wrapper::-webkit-scrollbar {
    display: none;
}

.filter-tabs {
    display: flex;
    gap: 0;
    min-width: max-content;
}

.filter-tab {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    padding: var(--spacing-4) var(--spacing-5);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    font-family: var(--font-family);
    color: var(--text-secondary);
    background: transparent;
    border: none;
    border-bottom: 3px solid transparent;
    cursor: pointer;
    white-space: nowrap;
    transition: all var(--transition-fast);
    position: relative;
}

.filter-tab:hover {
    color: var(--text-primary);
    background: var(--bg-secondary);
}

.filter-tab.active {
    color: var(--primary-color);
    border-bottom-color: var(--primary-color);
    font-weight: var(--font-weight-semibold);
}

.filter-count {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    background: var(--bg-tertiary);
    padding: 1px 7px;
    border-radius: var(--radius-full);
    font-weight: var(--font-weight-normal);
    min-width: 20px;
    text-align: center;
}

.filter-tab.active .filter-count {
    background: var(--primary-light);
    color: var(--primary-600);
}

/* --- Catalog Body --- */
.catalog-body {
    padding: var(--spacing-6) var(--spacing-8) var(--spacing-16);
}

.catalog-container {
    max-width: var(--max-width-xl);
    margin: 0 auto;
}

.results-info {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-5) 0;
    font-weight: var(--font-weight-medium);
}

/* --- Cards Grid --- */
.catalog-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: var(--spacing-4);
}

/* --- Card --- */
.catalog-card {
    background: var(--bg-primary);
    border: 1px solid var(--border-default);
    border-radius: var(--radius-sm);
    padding: var(--spacing-5);
    cursor: pointer;
    transition: all var(--transition-base);
    display: flex;
    flex-direction: column;
    min-height: 200px;
}

.catalog-card:hover {
    border-color: var(--primary-300);
    box-shadow: var(--shadow-card-hover);
    transform: translateY(-2px);
}

.card-top {
    display: flex;
    gap: var(--spacing-3);
    align-items: flex-start;
    margin-bottom: var(--spacing-3);
}

.card-icon {
    width: 40px;
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 24px;
    flex-shrink: 0;
    background: var(--bg-secondary);
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-light);
}

.card-title-section {
    flex: 1;
    min-width: 0;
}

.card-name {
    font-size: var(--font-size-base);
    font-weight: var(--font-weight-semibold);
    color: var(--text-primary);
    margin: 0;
    line-height: var(--line-height-tight);
}

.card-provider {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    margin-top: 2px;
    display: block;
}

.card-desc {
    font-size: var(--font-size-sm);
    color: var(--text-secondary);
    line-height: var(--line-height-relaxed);
    margin: 0 0 auto 0;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
}

.card-version {
    font-size: var(--font-size-xs);
    color: var(--text-tertiary);
    font-family: var(--font-family-mono);
    margin-top: var(--spacing-2);
}

/* --- Tags --- */
.card-tags {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-2);
    margin-top: var(--spacing-4);
    padding-top: var(--spacing-3);
    border-top: 1px solid var(--border-light);
}

.tag {
    font-size: 11px;
    font-weight: var(--font-weight-medium);
    padding: 2px 10px;
    border-radius: var(--radius-full);
    white-space: nowrap;
    letter-spacing: 0.02em;
}

.tag-category {
    background: var(--gray-100);
    color: var(--gray-600);
    border: 1px solid var(--gray-200);
}

.tag-free {
    background: var(--success-light);
    color: var(--success-dark);
    border: 1px solid #a7f3d0;
}

.tag-popular {
    background: var(--primary-light);
    color: var(--primary-700);
    border: 1px solid var(--primary-200);
    text-transform: uppercase;
    font-size: 10px;
    font-weight: var(--font-weight-bold);
    letter-spacing: 0.06em;
}

.tag-new {
    background: var(--accent-amber-light);
    color: #92400e;
    border: 1px solid #fcd34d;
    font-size: 10px;
    font-weight: var(--font-weight-bold);
    letter-spacing: 0.06em;
}

.tag-soon {
    background: #f8fafc;
    color: #64748b;
    border: 1px solid #e2e8f0;
    font-size: 10px;
    font-weight: var(--font-weight-bold);
    letter-spacing: 0.06em;
    text-transform: uppercase;
}

/* --- Empty State --- */
.empty-state {
    text-align: center;
    padding: var(--spacing-16) var(--spacing-8);
    color: var(--text-secondary);
}

.empty-icon {
    font-size: 48px;
    margin-bottom: var(--spacing-4);
}

.empty-state h3 {
    font-size: var(--font-size-lg);
    color: var(--text-primary);
    margin: 0 0 var(--spacing-2) 0;
}

.empty-state p {
    margin: 0;
    font-size: var(--font-size-sm);
}

/* --- Responsive --- */
@media (max-width: 1200px) {
    .catalog-grid {
        grid-template-columns: repeat(2, 1fr);
    }
}

@media (max-width: 768px) {
    .catalog-header {
        padding: var(--spacing-6) var(--spacing-4) var(--spacing-5);
    }

    .catalog-header-content {
        padding: 0;
    }

    .catalog-title {
        font-size: var(--font-size-2xl);
    }

    .catalog-search-wrapper {
        max-width: 100%;
    }

    .filter-tabs-wrapper {
        padding: 0 var(--spacing-4);
    }

    .catalog-body {
        padding: var(--spacing-4) var(--spacing-4) var(--spacing-12);
    }

    .catalog-grid {
        grid-template-columns: 1fr;
    }

    .catalog-card {
        min-height: auto;
    }
}
</style>

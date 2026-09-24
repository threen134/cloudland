<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { RouterView, RouterLink, useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '../../stores/auth'
import { useTenantStore } from '../../stores/tenant'
import { useRegionStore } from '../../stores/region'
import { useI18n } from 'vue-i18n'
import { setLanguage, getCurrentLanguage, SUPPORTED_LANGUAGES, LANGUAGE_LABEL_KEYS, type Language } from '../../locales'
import { alarmEventsApi } from '../../api/alarmEvents'
import { authApi } from '../../api/auth'
import { setAuthToken, beginTokenSwitch } from '../../api/client'
import { useToast } from '../../composables/useToast'
import { errorMessage } from '../../utils/error'
import {
    LayoutDashboard,
    Database,
    Server,
    HardDrive,
    Network,
    Shield,
    Globe2,
    Layers,
    GitFork,
    Key,
    Disc,
    Settings,
    LogOut,
    Bell,
    User,
    Cloud,
    ChevronDown,
    ChevronRight,
    SquareStack,
    Monitor,
    Users,
    Globe,
    Languages,
    PanelLeftClose,
    PanelLeftOpen,
    HelpCircle,
    CreditCard,
    BookOpen,
    LifeBuoy,
    ShoppingBag,
    MapPin,
    ArrowRightLeft,
    AlertTriangle,
    Building2,
    BellRing,
    MessageSquare,
    ShieldAlert,
    ShieldCheck,
} from 'lucide-vue-next'

const auth = useAuthStore()
const tenant = useTenantStore()
const region = useRegionStore()
const router = useRouter()
const route = useRoute()
const { t } = useI18n()
const toast = useToast()
const isSidebarCollapsed = ref(false)
const isSwitchingRegion = ref(false)

const toggleSidebar = () => {
    isSidebarCollapsed.value = !isSidebarCollapsed.value
}

const docsUrl = '/docs/'

// Ensure sections are expanded when collapsing sidebar to show icons
watch(isSidebarCollapsed, (collapsed) => {
    if (collapsed) {
        // Optional: you might want to auto-expand all or keep user state.
        // For 'show small icons of sub-pages', we need them visible.
    }
})

const currentLang = computed(() => getCurrentLanguage())

// Collapsible menu sections
const expandedSections = ref<string[]>(['auth', 'compute', 'network', 'alerting', 'admin'])
const activeDropdown = ref<string | null>(null)

const toggleSection = (section: string) => {
    const index = expandedSections.value.indexOf(section)
    if (index === -1) {
        expandedSections.value.push(section)
    } else {
        expandedSections.value.splice(index, 1)
    }
}

const isExpanded = (section: string) => expandedSections.value.includes(section)

const handleSwitchLanguage = (lang: Language) => {
    setLanguage(lang)
    activeDropdown.value = null
}

const handleSwitchRegion = async (regionId: string) => {
    if (isSwitchingRegion.value) return
    isSwitchingRegion.value = true
    const endSwitch = beginTokenSwitch()
    try {
        const response = await authApi.switchRegion(regionId)
        const newToken = response?.access_token
        if (newToken) {
            setAuthToken(newToken)
        }
        region.setCurrentRegion(regionId)
        activeDropdown.value = null
    } catch (err) {
        console.error('Failed to switch region:', err)
        toast.error(t('messages.error'))
    } finally {
        endSwitch()
        isSwitchingRegion.value = false
    }
}

const handleSwitchOrg = async (orgId: string) => {
    try {
        await tenant.switchOrg(orgId)
        activeDropdown.value = null
    } catch (err) {
        console.error('Failed to switch org:', err)
    }
}

// Global data refresh on context change
watch([() => tenant.currentOrgId, () => region.currentRegionId], () => {
    fetchFiringCount()
})

// Page title based on route
const pageTitle = computed(() => {
    const titles: Record<string, string> = {
        dashboard: t('dashboard.overview.title'),
        activities: t('dashboard.activityPage.title'),
        instances: t('dashboard.instances'),
        'instance-detail': t('dashboard.instances'),
        volumes: t('dashboard.volumes'),
        'volume-detail': t('dashboard.volumes'),
        images: t('dashboard.images'),
        'image-detail': t('dashboard.images'),
        vpcs: t('dashboard.vpcs'),
        'vpc-detail': t('dashboard.vpcs'),
        subnets: t('dashboard.subnets'),
        'subnet-detail': t('dashboard.subnets'),
        'floating-ips': t('dashboard.floatingIPs'),
        'floating-ip-detail': t('dashboard.floatingIPs'),
        'security-groups': t('dashboard.securityGroups'),
        'security-group-detail': t('dashboard.securityGroups'),
        'load-balancers': t('dashboard.loadBalancers'),
        'load-balancer-detail': t('dashboard.loadBalancers'),
        'vpn-gateways': t('dashboard.vpnGateways'),
        'vpn-gateway-detail': t('dashboard.vpnGateways'),
        'ssh-keys': t('dashboard.sshKeys'),
        users: t('dashboard.users'),
        'user-detail': t('dashboard.users'),
        orgs: t('dashboard.organizations'),
        'org-detail': t('dashboard.organizations'),
        keys: t('dashboard.sshKeys'),
        'dashboard-marketplace': t('nav.marketplace'),
        zones: t('dashboard.zones'),
        'zone-detail': t('dashboard.zones'),
        hypervisors: t('dashboard.hypervisors'),
        'hypervisor-detail': t('dashboard.hypervisors'),
        'storage-pools': t('storage.poolsPage'),
        'storage-pool-detail': t('storage.poolsPage'),
        migrations: t('dashboard.migrations'),
        'migration-detail': t('dashboard.migrations'),
        alarms: t('dashboard.alarms'),
        'alarm-detail': t('dashboard.alarms'),
        regions: t('dashboard.regions'),
        'notification-channels': t('dashboard.notificationChannels'),
        'alarm-events': t('dashboard.alarmEvents'),
        'vm-alarm-rules': t('dashboard.vmAlarmRules.title'),
        settings: t('settings.title'),
    }
    return titles[route.name as string] || t('nav.dashboard')
})

const handleEditUser = () => {
    // Navigate to user edit page or modal (placeholder)
    console.log('Edit User clicked')
}

const handleLogout = () => {
    auth.logout()
    tenant.clear()
    region.clear()
    router.push('/')
}

// Alarm firing count badge
const firingCount = ref(0)
const fetchFiringCount = async () => {
    try {
        const res = await alarmEventsApi.getSummary()
        firingCount.value = res.total_firing || 0
    } catch {
        firingCount.value = 0
    }
}

// Refresh firing count every 60s
let firingTimer: ReturnType<typeof setInterval> | null = null
onMounted(async () => {
    try {
        // Must await: switchOrg() issues a new token and revokes the old one.
        // Resources loaded after this point use the org-scoped token.
        await tenant.fetchOrganizations()
    } catch (err) {
        console.error('[Layout] fetchOrganizations failed, forcing logout:', errorMessage(err, String(err)))
        handleLogout()
        return
    }
    Promise.all([region.fetchRegions(), fetchFiringCount()])
    firingTimer = setInterval(fetchFiringCount, 60000)
})
onUnmounted(() => {
    if (firingTimer) clearInterval(firingTimer)
})
</script>

<template>
    <div class="dashboard-layout">
        <!-- Sidebar -->
        <aside class="sidebar" :class="{ collapsed: isSidebarCollapsed }">
            <div class="sidebar-header">
                <RouterLink to="/" class="logo-link">
                    <Cloud :size="28" class="logo-icon" />
                    <span class="logo-text">{{ $t('site.name') }}</span>
                </RouterLink>
                <button class="collapse-btn" @click="toggleSidebar">
                    <component :is="isSidebarCollapsed ? PanelLeftOpen : PanelLeftClose" :size="18" />
                </button>
            </div>

            <nav class="sidebar-nav">
                <!-- Overview -->
                <RouterLink to="/dashboard" class="nav-item" exact-active-class="active">
                    <LayoutDashboard :size="18" />
                    <span>{{ $t('nav.dashboard') }}</span>
                </RouterLink>

                <!-- Marketplace Link -->
                <RouterLink to="/dashboard/marketplace" class="nav-item" active-class="active">
                    <ShoppingBag :size="18" />
                    <span>{{ $t('nav.marketplace') }}</span>
                </RouterLink>

                <!-- Authorizations Section -->
                <div class="nav-section">
                    <button class="section-header" @click="toggleSection('auth')">
                        <span class="section-title">{{ $t('dashboard.authorizations') }}</span>
                        <component
                            :is="isExpanded('auth') ? ChevronDown : ChevronRight"
                            :size="14"
                            class="section-chevron"
                        />
                    </button>
                    <div v-show="isExpanded('auth') || isSidebarCollapsed" class="section-items">
                        <RouterLink to="/dashboard/users" class="nav-item" active-class="active">
                            <Users :size="18" />
                            <span>{{ $t('dashboard.users') }}</span>
                        </RouterLink>
                        <RouterLink
                            v-if="auth.user?.is_superuser"
                            to="/dashboard/orgs"
                            class="nav-item"
                            active-class="active"
                        >
                            <Building2 :size="18" />
                            <span>{{ $t('dashboard.organizations') }}</span>
                        </RouterLink>
                    </div>
                </div>

                <!-- Compute Section -->
                <div class="nav-section">
                    <button class="section-header" @click="toggleSection('compute')">
                        <span class="section-title">{{ $t('dashboard.compute') }}</span>
                        <component
                            :is="isExpanded('compute') ? ChevronDown : ChevronRight"
                            :size="14"
                            class="section-chevron"
                        />
                    </button>
                    <div v-show="isExpanded('compute') || isSidebarCollapsed" class="section-items">
                        <RouterLink to="/dashboard/instances" class="nav-item" active-class="active">
                            <Monitor :size="18" />
                            <span>{{ $t('dashboard.instances') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/volumes" class="nav-item" active-class="active">
                            <HardDrive :size="18" />
                            <span>{{ $t('dashboard.volumes') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/images" class="nav-item" active-class="active">
                            <Disc :size="18" />
                            <span>{{ $t('dashboard.images') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/flavors" class="nav-item" active-class="active">
                            <SquareStack :size="18" />
                            <span>{{ $t('dashboard.flavors') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/keys" class="nav-item" active-class="active">
                            <Key :size="18" />
                            <span>{{ $t('dashboard.sshKeys') }}</span>
                        </RouterLink>
                    </div>
                </div>

                <!-- Network Section -->
                <div class="nav-section">
                    <button class="section-header" @click="toggleSection('network')">
                        <span class="section-title">{{ $t('dashboard.network') }}</span>
                        <component
                            :is="isExpanded('network') ? ChevronDown : ChevronRight"
                            :size="14"
                            class="section-chevron"
                        />
                    </button>
                    <div v-show="isExpanded('network') || isSidebarCollapsed" class="section-items">
                        <RouterLink to="/dashboard/vpcs" class="nav-item" active-class="active">
                            <Layers :size="18" />
                            <span>{{ $t('dashboard.vpcs') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/subnets" class="nav-item" active-class="active">
                            <Network :size="18" />
                            <span>{{ $t('dashboard.subnets') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/floating-ips" class="nav-item" active-class="active">
                            <Globe :size="18" />
                            <span>{{ $t('dashboard.floatingIPs') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/security-groups" class="nav-item" active-class="active">
                            <Shield :size="18" />
                            <span>{{ $t('dashboard.securityGroups') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/load-balancers" class="nav-item" active-class="active">
                            <GitFork :size="18" />
                            <span>{{ $t('dashboard.loadBalancers') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/vpn-gateways" class="nav-item" active-class="active">
                            <ShieldCheck :size="18" />
                            <span>{{ $t('dashboard.vpnGateways') }}</span>
                        </RouterLink>
                    </div>
                </div>

                <!-- Alerting Section -->
                <div class="nav-section">
                    <button class="section-header" @click="toggleSection('alerting')">
                        <span class="section-title"
                            >{{ $t('dashboard.alerting') }}
                            <span v-if="firingCount > 0" class="firing-badge">{{ firingCount }}</span>
                        </span>
                        <component
                            :is="isExpanded('alerting') ? ChevronDown : ChevronRight"
                            :size="14"
                            class="section-chevron"
                        />
                    </button>
                    <div v-show="isExpanded('alerting') || isSidebarCollapsed" class="section-items">
                        <RouterLink to="/dashboard/alarm-events" class="nav-item" active-class="active">
                            <BellRing :size="18" />
                            <span>{{ $t('dashboard.alarmEvents') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/vm-alarm-rules" class="nav-item" active-class="active">
                            <ShieldAlert :size="18" />
                            <span>{{ $t('dashboard.vmAlarmRules.title') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/notification-channels" class="nav-item" active-class="active">
                            <MessageSquare :size="18" />
                            <span>{{ $t('dashboard.notificationChannels') }}</span>
                        </RouterLink>
                    </div>
                </div>

                <!-- Administration Section (Superadmin Only) -->
                <div class="nav-section" v-if="auth.user?.is_superuser">
                    <button class="section-header" @click="toggleSection('admin')">
                        <span class="section-title">{{ $t('dashboard.administration') }}</span>
                        <component
                            :is="isExpanded('admin') ? ChevronDown : ChevronRight"
                            :size="14"
                            class="section-chevron"
                        />
                    </button>
                    <div v-show="isExpanded('admin') || isSidebarCollapsed" class="section-items">
                        <RouterLink to="/dashboard/regions" class="nav-item" active-class="active">
                            <Globe2 :size="18" />
                            <span>{{ $t('dashboard.regions') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/zones" class="nav-item" active-class="active">
                            <MapPin :size="18" />
                            <span>{{ $t('dashboard.zones') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/hypervisors" class="nav-item" active-class="active">
                            <Server :size="18" />
                            <span>{{ $t('dashboard.hypervisors') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/storage-pools" class="nav-item" active-class="active">
                            <Database :size="18" />
                            <span>{{ $t('storage.poolsPage') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/migrations" class="nav-item" active-class="active">
                            <ArrowRightLeft :size="18" />
                            <span>{{ $t('dashboard.migrations') }}</span>
                        </RouterLink>
                        <RouterLink to="/dashboard/alarms" class="nav-item" active-class="active">
                            <AlertTriangle :size="18" />
                            <span>{{ $t('dashboard.alarms') }}</span>
                        </RouterLink>
                    </div>
                </div>

                <!-- Settings (SuperAdmin only) -->
                <div v-if="auth.user?.is_superuser" class="nav-divider"></div>
                <RouterLink
                    v-if="auth.user?.is_superuser"
                    to="/dashboard/settings"
                    class="nav-item"
                    active-class="active"
                >
                    <Settings :size="18" />
                    <span>{{ $t('settings.title') }}</span>
                </RouterLink>
            </nav>
        </aside>

        <!-- Main Content -->
        <main class="main-content">
            <!-- Header -->
            <header class="header">
                <div class="header-left">
                    <h2>{{ pageTitle }}</h2>
                </div>
                <!-- <div class="header-center">
        </div> -->
                <div class="header-right">
                    <!-- Org Switcher -->
                    <div
                        class="header-dropdown"
                        @mouseenter="activeDropdown = 'org'"
                        @mouseleave="activeDropdown = null"
                    >
                        <button class="lang-toggle-btn">
                            <Building2 :size="16" />
                            <span>{{ tenant.currentOrg?.name || $t('dashboard.org.selectOrg') }}</span>
                            <ChevronDown :size="14" />
                        </button>
                        <div class="dropdown-menu-portal" v-show="activeDropdown === 'org'">
                            <div
                                v-if="tenant.organizations.length === 0"
                                class="dropdown-item-portal"
                                style="color: var(--text-tertiary); cursor: default"
                            >
                                {{ $t('dashboard.org.noOrgs') }}
                            </div>
                            <div
                                v-else
                                v-for="org in tenant.organizations"
                                :key="org.id"
                                class="dropdown-item-portal"
                                :class="{ active: String(tenant.currentOrgId) === String(org.id) }"
                                @click="handleSwitchOrg(String(org.id))"
                            >
                                {{ org.name }}
                            </div>
                        </div>
                    </div>
                    <!-- Region Switcher -->
                    <div
                        class="header-dropdown"
                        @mouseenter="activeDropdown = 'region'"
                        @mouseleave="activeDropdown = null"
                    >
                        <button class="lang-toggle-btn">
                            <Globe2 :size="16" />
                            <span>{{
                                region.currentRegion
                                    ? $te('regions.' + region.currentRegion.name)
                                        ? $t('regions.' + region.currentRegion.name)
                                        : region.currentRegion.label || region.currentRegion.name
                                    : $t('dashboard.region')
                            }}</span>
                            <ChevronDown :size="14" />
                        </button>
                        <div class="dropdown-menu-portal" v-show="activeDropdown === 'region'">
                            <div
                                v-for="r in region.availableRegions"
                                :key="r.id"
                                class="dropdown-item-portal"
                                :class="{ active: region.currentRegionId === r.id, disabled: isSwitchingRegion }"
                                :style="isSwitchingRegion ? { opacity: '0.6', cursor: 'not-allowed' } : {}"
                                @click="handleSwitchRegion(r.id)"
                            >
                                {{ $te('regions.' + r.name) ? $t('regions.' + r.name) : r.label || r.name }}
                            </div>
                        </div>
                    </div>
                    <!-- Language Switcher -->
                    <div
                        class="header-dropdown"
                        @mouseenter="activeDropdown = 'lang'"
                        @mouseleave="activeDropdown = null"
                    >
                        <button class="lang-toggle-btn">
                            <Languages :size="16" />
                            <span>{{ $t(LANGUAGE_LABEL_KEYS[currentLang]) }}</span>
                            <ChevronDown :size="14" />
                        </button>
                        <div class="dropdown-menu-portal" v-show="activeDropdown === 'lang'">
                            <div
                                v-for="lang in SUPPORTED_LANGUAGES"
                                :key="lang"
                                class="dropdown-item-portal"
                                :class="{ active: currentLang === lang }"
                                @click="handleSwitchLanguage(lang)"
                            >
                                {{ $t(LANGUAGE_LABEL_KEYS[lang]) }}
                            </div>
                        </div>
                    </div>

                    <!-- Help Dropdown -->
                    <div
                        class="header-dropdown"
                        @mouseenter="activeDropdown = 'help'"
                        @mouseleave="activeDropdown = null"
                    >
                        <button class="icon-btn" :title="$t('nav.help')">
                            <HelpCircle :size="16" />
                        </button>
                        <div class="dropdown-menu-portal" v-show="activeDropdown === 'help'">
                            <div class="dropdown-item-portal">
                                <CreditCard :size="14" />
                                <span>{{ $t('nav.billing') }}</span>
                            </div>
                            <a class="dropdown-item-portal" :href="docsUrl" target="_blank" rel="noopener">
                                <BookOpen :size="14" />
                                <span>{{ $t('nav.docsAndApi') }}</span>
                            </a>
                            <div class="dropdown-item-portal">
                                <LifeBuoy :size="14" />
                                <span>{{ $t('nav.support') }}</span>
                            </div>
                        </div>
                    </div>

                    <button class="icon-btn" :title="$t('nav.notifications')">
                        <Bell :size="16" />
                    </button>
                    <div
                        class="header-dropdown"
                        @mouseenter="activeDropdown = 'user'"
                        @mouseleave="activeDropdown = null"
                    >
                        <div class="user-profile">
                            <div class="user-avatar">
                                <User :size="16" />
                            </div>
                            <span>{{ auth.user?.username || auth.user?.name || $t('dashboard.table.userName') }}</span>
                            <ChevronDown
                                :size="14"
                                style="margin-left: 4px; color: var(--text-secondary); opacity: 0.7"
                            />
                        </div>

                        <div class="dropdown-menu-portal" v-show="activeDropdown === 'user'">
                            <div class="dropdown-item-portal" @click="handleEditUser">
                                <div style="display: flex; align-items: center; gap: 8px">
                                    <User :size="14" />
                                    <span>{{ $t('actions.editUser') }}</span>
                                </div>
                            </div>
                            <div class="dropdown-item-portal" @click="handleLogout">
                                <div style="display: flex; align-items: center; gap: 8px">
                                    <LogOut :size="14" />
                                    <span>{{ $t('actions.logout') }}</span>
                                </div>
                            </div>
                        </div>
                    </div>
                </div>
            </header>

            <!-- Page Content -->
            <div class="page-content">
                <RouterView :key="`${String(route.name)}-${tenant.currentOrgId}-${region.currentRegionId}`" />
            </div>
        </main>
    </div>
</template>

<style scoped>
.dashboard-layout {
    display: flex;
    height: 100vh;
    background-color: var(--bg-secondary); /* Very light slate */
    color: var(--text-primary);
}

/* Sidebar (CoreUI-inspired Light Blue Theme) */
.sidebar {
    width: var(--sidebar-width);
    background-color: var(--bg-secondary); /* very soft blue-gray background */
    border-right: 1px solid rgba(0, 0, 0, 0.05);
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    transition: width 0.3s ease;
    z-index: var(--z-sidebar);
    box-shadow: 2px 0 10px rgba(0, 0, 0, 0.03);
}

.sidebar.collapsed {
    width: 70px;
}

.sidebar-header {
    height: var(--height-header);
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 18px;
    background-color: var(--bg-primary); /* White logo area for contrast */
    border-bottom: 1px solid rgba(0, 0, 0, 0.05);
    box-shadow: 0 2px 5px rgba(0, 0, 0, 0.02);
    z-index: 2;
}

.logo-link {
    display: flex;
    align-items: center;
    gap: 12px;
    text-decoration: none;
}

.logo-icon {
    color: var(--primary-hover); /* Strong Blue */
    filter: drop-shadow(0 2px 4px rgba(37, 99, 235, 0.2));
}

.logo-text {
    font-weight: 800;
    font-size: 1.35rem;
    letter-spacing: -0.03em;
    color: var(--gray-800);
    white-space: nowrap;
    transition: opacity 0.2s;
}

.sidebar.collapsed .logo-text {
    display: none;
    opacity: 0;
}

.collapse-btn {
    background: none;
    border: none;
    color: var(--text-secondary);
    cursor: pointer;
    padding: 4px;
    border-radius: 4px;
    display: flex;
    align-items: center;
    justify-content: center;
}

.collapse-btn:hover {
    background-color: var(--bg-tertiary);
    color: var(--primary-500);
}

.sidebar.collapsed .sidebar-header {
    justify-content: center;
    padding: 0;
}

.sidebar.collapsed .logo-link {
    display: none;
}

.sidebar-nav {
    flex: 1;
    padding: 16px 12px;
    overflow-y: auto;
}

/* Navigation Items */
.nav-item {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 16px;
    min-height: 42px;
    color: var(--text-secondary); /* Slate 600 */
    text-decoration: none;
    transition: all 0.2s ease;
    cursor: pointer;
    border-radius: 6px;
    margin-bottom: 4px;
    font-size: 0.9rem;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
}

.sidebar.collapsed .nav-item {
    justify-content: center;
    padding: 10px 0;
    width: 46px;
    margin: 0 auto 4px auto;
}

.sidebar.collapsed .nav-item span {
    display: none;
}

.nav-item:hover {
    background-color: var(--gray-200); /* Slight gray/blue hover */
    color: var(--gray-800);
}

/* 选中项原先是实心蓝渐变块 + 投影，视觉重量比页面上任何元素都大，
   眼睛总被导航拽走。改为淡主色底 + 左侧 3px 主色条：既能一眼定位，
   又不和内容抢，语言也和详情页的卡片标题、资源条一致 */
.nav-item.active {
    background: var(--primary-50);
    color: var(--primary-700);
    font-weight: 600;
    box-shadow: inset 3px 0 0 var(--primary-color);
}

.nav-item.active:hover {
    background: var(--primary-light);
    color: var(--primary-700);
}

.nav-item.active svg {
    color: var(--primary-color);
}

/* 收起时导航项是居中的小方块，左侧竖条会显得突兀，只留淡底 */
.sidebar.collapsed .nav-item.active {
    box-shadow: none;
}

/* Collapsible Sections */
.nav-section {
    margin-top: 24px;
    margin-bottom: 8px;
}

.section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    padding: 0 16px;
    margin-bottom: 8px;
    background: none;
    border: none;
    color: var(--text-tertiary); /* Lighter slate for headers */
    font-size: 0.75rem;
    font-weight: 800;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    cursor: pointer;
    white-space: nowrap;
}

.sidebar.collapsed .section-header {
    display: none;
}

.section-header:hover {
    color: var(--gray-500);
}

.firing-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 18px;
    height: 18px;
    padding: 0 5px;
    margin-left: 6px;
    font-size: 11px;
    font-weight: 600;
    line-height: 1;
    color: white;
    background: var(--error-color);
    border-radius: 9px;
}

.section-items {
    margin-top: 2px;
}

/* Indented sub-items */
.section-items .nav-item {
    padding-left: 20px;
    font-size: 0.9rem;
}

.sidebar.collapsed .section-items .nav-item {
    padding-left: 0;
}

.nav-divider {
    height: 1px;
    background-color: var(--gray-200);
    margin: 16px 16px;
}

/* Sidebar Footer */
.sidebar-footer {
    padding: 16px;
    border-top: 1px solid rgba(0, 0, 0, 0.05);
    background-color: var(--bg-secondary);
}

.logout-btn {
    color: var(--gray-500);
    width: 100%;
    justify-content: flex-start;
    transition: all 0.2s;
}

.logout-btn:hover {
    background-color: var(--error-light);
    color: var(--error-color);
}

/* Main Content Area */
.main-content {
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
    min-width: 0;
    background-color: var(--bg-secondary);
}

/* Header */
.header {
    height: var(--height-header);
    background-color: var(--bg-primary); /* White header */
    /* border-bottom: 1px solid var(--border-light); */
    box-shadow: var(--shadow-sm); /* Subtle shadow separating header */
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0 32px;
    color: var(--text-primary);
    flex-shrink: 0;
    z-index: var(--z-fixed);
}

.header h2 {
    margin: 0;
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--gray-800);
}

.header-center {
    display: flex;
    align-items: center;
    gap: 24px;
}

.context-indicator {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-full);
    font-size: 0.8125rem;
    color: var(--text-secondary);
    font-weight: 500;
}

.header-right {
    display: flex;
    align-items: center;
    height: 100%;
    gap: 4px;
}

.icon-btn {
    background: transparent;
    border: none;
    color: var(--gray-400);
    cursor: pointer;
    width: 36px;
    height: 36px;
    border-radius: var(--radius-full);
    transition: all 0.2s;
    display: flex;
    align-items: center;
    justify-content: center;
}

.icon-btn:hover {
    background: var(--bg-tertiary);
    color: var(--primary-500);
}

.header-dropdown {
    position: relative;
    display: flex;
    align-items: center;
}

.lang-toggle-btn {
    display: flex;
    align-items: center;
    gap: 6px;
    background: none;
    border: none;
    padding: 0 10px;
    height: 36px;
    color: var(--text-secondary);
    font-size: 0.875rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s;
    border-radius: var(--radius-md);
}

.lang-toggle-btn:hover {
    background-color: var(--bg-tertiary);
    color: var(--text-primary);
}

.dropdown-menu-portal {
    position: absolute;
    top: 100%;
    right: 0;
    width: 200px;
    background-color: var(--bg-primary);
    border: 1px solid var(--border-light);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-lg);
    z-index: var(--z-dropdown);
    padding: 6px;
    margin-top: 10px;
}

.dropdown-menu-portal::before {
    content: '';
    position: absolute;
    top: -10px;
    left: 0;
    width: 100%;
    height: 10px;
    background: transparent;
}

.dropdown-item-portal {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
    font-size: 0.875rem;
    color: var(--text-secondary);
    cursor: pointer;
    border-radius: var(--radius-md);
    transition: all 0.2s;
    text-decoration: none;
}

.dropdown-item-portal:hover {
    background-color: var(--bg-tertiary);
    color: var(--text-primary);
}

.dropdown-item-portal.active {
    background-color: var(--primary-50);
    color: var(--primary-600);
    font-weight: 500;
}

.user-profile {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 0 10px 0 16px;
    height: 36px;
    border-left: 1px solid var(--border-light);
    cursor: pointer;
    font-size: 0.875rem;
    font-weight: 500;
    color: var(--text-primary);
    border-radius: var(--radius-md);
    transition: all 0.2s;
}

.user-profile:hover {
    background-color: var(--bg-tertiary);
}

.user-profile:hover .user-avatar {
    box-shadow: 0 0 0 2px var(--primary-100);
}

.user-avatar {
    width: 32px;
    height: 32px;
    border-radius: 50%;
    background: var(--primary-100);
    color: var(--primary-600);
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all 0.2s;
}

/* Page Content */
.page-content {
    flex: 1;
    padding: var(--spacing-6);
    overflow-y: auto;
    background-color: var(--bg-secondary);
}

/* Responsive */
@media screen and (max-width: 1024px) {
    .sidebar {
        width: 200px;
    }

    .context-indicator span {
        display: none;
    }
}

@media screen and (max-width: 768px) {
    .sidebar {
        position: fixed;
        left: -100%;
        z-index: var(--z-fixed);
        transition: left 0.3s ease;
    }

    .sidebar.open {
        left: 0;
    }

    .header-center {
        display: none;
    }
}
</style>

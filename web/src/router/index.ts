import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = createRouter({
    history: createWebHistory(import.meta.env.BASE_URL),
    routes: [
        {
            path: '/',
            name: 'home',
            component: () => import('../views/Home.vue')
        },
        {
            path: '/console/:id',
            name: 'instance-console',
            component: () => import('../views/dashboard/InstanceConsole.vue'),
            meta: { requiresAuth: true }
        },
        {
            path: '/login',
            name: 'login',
            component: () => import('../views/auth/Login.vue')
        },
        {
            path: '/register',
            name: 'register',
            component: () => import('../views/auth/Register.vue')
        },
        {
            path: '/register/success',
            name: 'register-success',
            component: () => import('../views/auth/RegisterSuccess.vue')
        },
        {
            path: '/activate',
            name: 'activate',
            component: () => import('../views/auth/ActivateAccount.vue')
        },
        {
            path: '/invite/accept',
            name: 'accept-invitation',
            component: () => import('../views/auth/AcceptInvitation.vue')
        },
        {
            path: '/api-reference',
            name: 'api-reference',
            component: () => import('../views/ApiReference.vue')
        },

        {
            path: '/marketplace',
            name: 'marketplace',
            component: () => import('../views/marketplace/Marketplace.vue')
        },
        {
            path: '/payment',
            name: 'payment',
            component: () => import('../views/marketplace/Payment.vue'),
            meta: { requiresAuth: true }
        },
        {
            path: '/dashboard',
            component: () => import('../views/dashboard/Layout.vue'),
            meta: { requiresAuth: true },
            children: [
                {
                    path: '',
                    name: 'dashboard',
                    component: () => import('../views/dashboard/Overview.vue')
                },
                {
                    path: 'marketplace',
                    name: 'dashboard-marketplace',
                    component: () => import('../views/marketplace/Marketplace.vue')
                },
                // Authorizations
                {
                    path: 'users',
                    name: 'users',
                    component: () => import('../views/dashboard/UserList.vue')
                },
                {
                    path: 'users/:id',
                    name: 'user-detail',
                    component: () => import('../views/dashboard/UserDetail.vue')
                },
                {
                    path: 'orgs',
                    name: 'orgs',
                    component: () => import('../views/dashboard/OrgList.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'orgs/:id',
                    name: 'org-detail',
                    component: () => import('../views/dashboard/OrgDetail.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'keys',
                    name: 'keys', // SSH Keys real path
                    component: () => import('../views/dashboard/SSHKeys.vue')
                },
                // SSH key detail page removed as requested
                // {
                //     path: 'keys/:id',
                //     name: 'ssh-key-detail',
                //     component: () => import('../views/dashboard/SSHKeyDetail.vue')
                // },
                // Compute Resources
                {
                    path: 'instances',
                    name: 'instances',
                    component: () => import('../views/dashboard/InstanceList.vue')
                },
                {
                    path: 'instances/:id',
                    name: 'instance-detail',
                    component: () => import('../views/dashboard/InstanceDetail.vue')
                },
                {
                    path: 'volumes',
                    name: 'volumes',
                    component: () => import('../views/dashboard/VolumeList.vue')
                },
                {
                    path: 'volumes/:id',
                    name: 'volume-detail',
                    component: () => import('../views/dashboard/VolumeDetail.vue')
                },
                {
                    path: 'images',
                    name: 'images',
                    component: () => import('../views/dashboard/Images.vue')
                },
                {
                    path: 'images/:id',
                    name: 'image-detail',
                    component: () => import('../views/dashboard/ImageDetail.vue')
                },
                {
                    path: 'flavors',
                    name: 'flavors',
                    component: () => import('../views/dashboard/FlavorList.vue')
                },
                {
                    path: 'ssh-keys',
                    name: 'ssh-keys-redirect',
                    redirect: { name: 'keys' }
                },
                // Network Resources
                {
                    path: 'vpcs',
                    name: 'vpcs',
                    component: () => import('../views/dashboard/VPCList.vue')
                },
                {
                    path: 'vpcs/:id',
                    name: 'vpc-detail',
                    component: () => import('../views/dashboard/VPCDetail.vue')
                },
                {
                    path: 'subnets',
                    name: 'subnets',
                    component: () => import('../views/dashboard/SubnetList.vue')
                },
                {
                    path: 'subnets/:id',
                    name: 'subnet-detail',
                    component: () => import('../views/dashboard/SubnetDetail.vue')
                },
                {
                    path: 'floating-ips',
                    name: 'floating-ips',
                    component: () => import('../views/dashboard/FloatingIPList.vue')
                },
                {
                    path: 'floating-ips/:id',
                    name: 'floating-ip-detail',
                    component: () => import('../views/dashboard/FloatingIPDetail.vue')
                },
                {
                    path: 'security-groups',
                    name: 'security-groups',
                    component: () => import('../views/dashboard/SecurityGroups.vue')
                },
                {
                    path: 'security-groups/:id',
                    name: 'security-group-detail',
                    component: () => import('../views/dashboard/SecurityGroupDetail.vue')
                },
                {
                    path: 'load-balancers',
                    name: 'load-balancers',
                    component: () => import('../views/dashboard/LoadBalancers.vue')
                },
                {
                    path: 'load-balancers/:id',
                    name: 'load-balancer-detail',
                    component: () => import('../views/dashboard/LoadBalancerDetail.vue')
                },
                // Administration (Superadmin only)
                {
                    path: 'regions',
                    name: 'regions',
                    component: () => import('../views/dashboard/RegionList.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'zones',
                    name: 'zones',
                    component: () => import('../views/dashboard/ZoneList.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'zones/:name',
                    name: 'zone-detail',
                    component: () => import('../views/dashboard/ZoneDetail.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'hypervisors',
                    name: 'hypervisors',
                    component: () => import('../views/dashboard/HypervisorList.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'hypervisors/:id',
                    name: 'hypervisor-detail',
                    component: () => import('../views/dashboard/HypervisorDetail.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'migrations',
                    name: 'migrations',
                    component: () => import('../views/dashboard/MigrationList.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'migrations/:id',
                    name: 'migration-detail',
                    component: () => import('../views/dashboard/MigrationDetail.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'alarms',
                    name: 'alarms',
                    component: () => import('../views/dashboard/AlarmList.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                {
                    path: 'alarms/:id',
                    name: 'alarm-detail',
                    component: () => import('../views/dashboard/AlarmDetail.vue'),
                    meta: { requiresSuperAdmin: true }
                },
                // Notification & Alarm Events (all users)
                {
                    path: 'vm-alarm-rules',
                    name: 'vm-alarm-rules',
                    component: () => import('../views/dashboard/VMAlarmRules.vue')
                },
                {
                    path: 'notification-channels',
                    name: 'notification-channels',
                    component: () => import('../views/dashboard/NotificationChannels.vue')
                },
                {
                    path: 'alarm-events',
                    name: 'alarm-events',
                    component: () => import('../views/dashboard/AlarmEvents.vue')
                },
                // Settings (SuperAdmin only)
                {
                    path: 'settings',
                    name: 'settings',
                    component: () => import('../views/dashboard/SystemSettings.vue'),
                    meta: { requiresSuperAdmin: true }
                }
            ]
        },
        {
            path: '/:pathMatch(.*)*',
            redirect: '/'
        }
    ]
})

router.beforeEach((to, _from, next) => {
    const auth = useAuthStore()

    if (to.meta.requiresAuth && !auth.user && !auth.isLoading) {
        return next({ name: 'login' })
    }

    // Check superadmin requirement
    if (to.meta.requiresSuperAdmin && !auth.user?.is_superuser) {
        return next({ name: 'dashboard' })
    }

    next()
})

export default router

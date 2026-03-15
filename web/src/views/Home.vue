<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Navbar from '../components/Navbar.vue'
import { 
    Zap, 
    Shield, 
    Globe, 
    Server, 
    HardDrive, 
    Network, 
    Check, 
    ArrowRight,
    Cpu,
    Database,
    Clock,
    Headphones,
    Cloud
} from 'lucide-vue-next'

const { t, tm } = useI18n()
const router = useRouter()

// Pricing plans (with translation keys)
const plans = [
    {
        nameKey: 'pricing.entryLevel',
        descKey: 'pricing.entryLevelDesc',
        price: '$5.00',
        popular: false,
        specs: [
            { labelKey: 'specs.cpu', value: '1 Core' },
            { labelKey: 'specs.ram', value: '1 GB' },
            { labelKey: 'specs.storage', value: '25 GB SSD' },
            { labelKey: 'specs.bandwidth', value: '1 TB' },
            { labelKey: 'specs.ip', value: '1 IP' },
        ]
    },
    {
        nameKey: 'pricing.advanced',
        descKey: 'pricing.advancedDesc',
        price: '$15.00',
        popular: false,
        specs: [
            { labelKey: 'specs.cpu', value: '2 Cores' },
            { labelKey: 'specs.ram', value: '4 GB' },
            { labelKey: 'specs.storage', value: '80 GB SSD' },
            { labelKey: 'specs.bandwidth', value: '3 TB' },
            { labelKey: 'specs.ip', value: '1 IP' },
        ]
    },
    {
        nameKey: 'pricing.enterprise',
        descKey: 'pricing.enterpriseDesc',
        price: '$45.00',
        popular: true,
        specs: [
            { labelKey: 'specs.cpu', value: '4 Cores' },
            { labelKey: 'specs.ram', value: '8 GB' },
            { labelKey: 'specs.storage', value: '160 GB NVMe' },
            { labelKey: 'specs.bandwidth', value: '5 TB' },
            { labelKey: 'specs.ip', value: '2 IPs' },
        ]
    },
    {
        nameKey: 'pricing.professional',
        descKey: 'pricing.professionalDesc',
        price: '$80.00',
        popular: false,
        specs: [
            { labelKey: 'specs.cpu', value: '8 Cores' },
            { labelKey: 'specs.ram', value: '16 GB' },
            { labelKey: 'specs.storage', value: '320 GB NVMe' },
            { labelKey: 'specs.bandwidth', value: 'Unlimited' },
            { labelKey: 'specs.ip', value: '3 IPs' },
        ]
    },
]

// Features
const features = [
    { icon: Cpu, titleKey: 'features.highPerformance', itemsKey: 'features.highPerformanceItems' },
    { icon: Zap, titleKey: 'features.flexible', itemsKey: 'features.flexibleItems' },
    { icon: Globe, titleKey: 'features.globalNetwork', itemsKey: 'features.globalNetworkItems' },
    { icon: Headphones, titleKey: 'features.support', itemsKey: 'features.supportItems' },
]

// Use cases
const useCases = [
    { image: '/images/blog_learning.png', titleKey: 'useCases.blogs', descKey: 'useCases.blogsDesc' },
    { image: '/images/corporate_ecommerce.png', titleKey: 'useCases.corporate', descKey: 'useCases.corporateDesc' },
    { image: '/images/dev_testing.png', titleKey: 'useCases.devTest', descKey: 'useCases.devTestDesc' },
    { image: '/images/api_microservices.png', titleKey: 'useCases.api', descKey: 'useCases.apiDesc' },
]

const stats = [
    { value: '10Gbps', labelKey: 'stats.networkSpeed' },
    { value: '30-Day', labelKey: 'stats.moneyBack' },
    { value: 'From $5', labelKey: 'stats.startingPrice' },
    { value: '99.9%', labelKey: 'stats.uptimeSLA' },
]

const goToMarketplace = () => {
    router.push('/marketplace')
}
</script>

<template>
  <div class="home-page">
    <Navbar />

    <!-- Hero Section -->
    <section class="hero">
      <div class="container">
        <div class="hero-badge">
          <span>{{ t('hero.badge') }}</span>
        </div>
        
        <h1 class="hero-title">
          {{ t('hero.title') }}<br />
          <span class="text-gradient-red">{{ t('hero.titleHighlight') }}</span>
        </h1>
        
        <p class="hero-subtitle text-inverse-secondary">
          {{ t('hero.subtitle') }}
        </p>

        <div class="hero-actions">
          <button class="btn btn-white btn-lg" @click="goToMarketplace">
            {{ t('hero.getStarted') }}
          </button>
          <button class="btn btn-secondary-white btn-lg" @click="goToMarketplace">
            {{ t('hero.viewPlans') }}
          </button>
        </div>

        <!-- Stats Cards -->
        <div class="stats-grid">
          <div v-for="stat in stats" :key="stat.labelKey" class="stat-card glass-panel">
            <div class="stat-value">{{ stat.value }}</div>
            <div class="stat-label">{{ t(stat.labelKey) }}</div>
          </div>
        </div>
      </div>
    </section>

    <!-- Pricing Section -->
    <section class="section pricing-section">
      <div class="container">
        <div class="section-header">
          <h2>{{ t('pricing.title') }}</h2>
          <p>{{ t('pricing.subtitle') }}</p>
        </div>

        <div class="pricing-grid">
          <div 
            v-for="plan in plans" 
            :key="plan.nameKey" 
            class="price-card"
            :class="{ popular: plan.popular }"
          >
            <span v-if="plan.popular" class="badge-popular">{{ t('pricing.popular') }}</span>
            <h3 class="plan-name">{{ t(plan.nameKey) }}</h3>
            <p class="plan-desc">{{ t(plan.descKey) }}</p>
            
            <ul class="spec-list">
              <li v-for="spec in plan.specs" :key="spec.labelKey">
                <span class="spec-label">{{ t(spec.labelKey) }}:</span>
                <span class="spec-value">{{ spec.value }}</span>
              </li>
            </ul>

            <div class="price">
              {{ plan.price }}<span>{{ t('pricing.perMonth') }}</span>
            </div>

            <button class="btn btn-primary" style="width: 100%;">
              {{ t('pricing.orderNow') }}
            </button>
          </div>
        </div>

        <div class="text-center mt-8">
          <button class="btn btn-secondary btn-lg" @click="goToMarketplace">
            {{ t('pricing.viewAll') }} <ArrowRight :size="16" />
          </button>
        </div>
      </div>
    </section>

    <!-- Features Section -->
    <section class="section section-bg">
      <div class="container">
        <div class="section-header">
          <h2>{{ t('features.title') }}</h2>
          <p>{{ t('features.subtitle') }}</p>
        </div>

        <div class="features-grid">
          <div v-for="feature in features" :key="feature.titleKey" class="feature-card">
            <div class="icon-box">
              <component :is="feature.icon" :size="28" />
            </div>
            <h4>{{ t(feature.titleKey) }}</h4>
            <ul class="check-list">
              <li v-for="(item, index) in (tm(feature.itemsKey) as any)" :key="index">
                <Check :size="16" class="check-icon" />
                <span>{{ item }}</span>
              </li>
            </ul>
          </div>
        </div>
      </div>
    </section>

    <!-- Use Cases Section -->
    <section class="section">
      <div class="container">
        <div class="section-header">
          <h2>{{ t('useCases.title') }}</h2>
          <p>{{ t('useCases.subtitle') }}</p>
        </div>

        <div class="scenarios-grid">
          <div v-for="useCase in useCases" :key="useCase.titleKey" class="scenario-card">
            <div class="card-image">
              <img :src="useCase.image" :alt="t(useCase.titleKey)" />
            </div>
            <div class="card-content">
              <h4>{{ t(useCase.titleKey) }}</h4>
              <p>{{ t(useCase.descKey) }}</p>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- CTA Section -->
    <section class="cta-section">
      <div class="container">
        <div class="cta-content">
          <h2>{{ t('cta.title') }}</h2>
          <p>{{ t('cta.subtitle') }}</p>
          <div class="cta-actions">
            <button class="btn btn-white btn-lg" @click="goToMarketplace">
              {{ t('cta.freeTrial') }}
            </button>
            <button class="btn btn-white btn-lg" @click="goToMarketplace">
              {{ t('cta.contactSales') }}
            </button>
          </div>
        </div>
      </div>
    </section>

    <!-- Footer -->
    <footer class="footer">
      <div class="container">
        <div class="footer-grid">
          <div class="footer-brand">
            <div class="logo">
              <Cloud :size="24" />
              <span>IBM Cloud China</span>
            </div>
            <p>{{ t('footer.tagline') }}</p>
          </div>
          <div class="footer-links">
            <h5>{{ t('footer.products') }}</h5>
            <a href="/marketplace">{{ t('products.cloudInstances') }}</a>
            <a href="/marketplace">{{ t('products.blockStorage') }}</a>
            <a href="/marketplace">{{ t('products.loadBalancers') }}</a>
            <a href="/marketplace">{{ t('products.floatingIPs') }}</a>
          </div>
          <div class="footer-links">
            <h5>{{ t('footer.resources') }}</h5>
            <a href="#">{{ t('nav.documentation') }}</a>
            <a href="#">{{ t('footer.apiReference') }}</a>
            <a href="#">{{ t('footer.statusPage') }}</a>
            <a href="#">{{ t('nav.support') }}</a>
          </div>
          <div class="footer-links">
            <h5>{{ t('footer.company') }}</h5>
            <a href="#">{{ t('footer.aboutUs') }}</a>
            <a href="#">{{ t('footer.blog') }}</a>
            <a href="#">{{ t('footer.careers') }}</a>
            <a href="#">{{ t('footer.contact') }}</a>
          </div>
        </div>
        <div class="footer-bottom">
          <p>{{ t('footer.copyright') }}</p>
        </div>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.home-page {
    background: var(--bg-primary);

    /* Scoped IBM Red Theme specifically for Home page */
    --primary-50: #fff1f1;
    --primary-100: #ffe3e3;
    --primary-200: #ffc6c6;
    --primary-300: #ff9d9d;
    --primary-400: #ff6464;
    --primary-500: #fa4d56;
    --primary-600: #da1e28;
    --primary-700: #a2191f;
    --primary-800: #750e13;
    --primary-900: #520408;

    --primary-color: #da1e28;
    --primary-hover: #fa4d56;
    --primary-active: #a2191f;
    --primary-light: #ffe3e3;
    --primary-gradient: linear-gradient(135deg, #da1e28 0%, #fa4d56 100%);
    
    /* Make the B2B borders sharper for index only */
    --radius-sm: 0.125rem;
    --radius-md: 0.25rem;
    --radius-lg: 0.375rem;
    --radius-xl: 0.5rem;
    --radius-2xl: 0.75rem;
}

.text-gradient-red {
    background: linear-gradient(to right, #da1e28, #fa4d56);
    -webkit-background-clip: text;
    background-clip: text;
    -webkit-text-fill-color: transparent;
}

.text-inverse-secondary {
    color: rgba(255, 255, 255, 0.8) !important;
}

.btn-secondary-white {
    background: transparent;
    border: 2px solid rgba(255, 255, 255, 0.3);
    color: var(--text-inverse);
}

.btn-secondary-white:hover {
    background: rgba(255, 255, 255, 0.1);
    border-color: var(--text-inverse);
}


.hero {
    padding: var(--spacing-20) 0 var(--spacing-24);
    text-align: center;
    background: linear-gradient(135deg, var(--primary-900) 0%, var(--primary-600) 100%);
    color: var(--text-inverse);
    position: relative;
    overflow: hidden;
}

.hero::before {
    content: '';
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: radial-gradient(circle at 50% 0%, rgba(255, 255, 255, 0.1) 0%, transparent 60%);
    pointer-events: none;
}

.hero-badge {
    display: inline-block;
    padding: var(--spacing-2) var(--spacing-5);
    background: rgba(255, 255, 255, 0.15);
    backdrop-filter: blur(4px);
    color: var(--text-inverse);
    border: 1px solid rgba(255, 255, 255, 0.2);
    font-size: var(--font-size-sm);
    font-weight: var(--font-weight-medium);
    border-radius: var(--radius-full);
    margin-bottom: var(--spacing-6);
}

.hero-title {
    font-size: var(--font-size-5xl);
    font-weight: var(--font-weight-bold);
    line-height: 1.1;
    margin-bottom: var(--spacing-6);
}

.hero-subtitle {
    font-size: var(--font-size-xl);
    color: var(--text-secondary);
    max-width: 600px;
    margin: 0 auto var(--spacing-8);
}

.hero-actions {
    display: flex;
    justify-content: center;
    gap: var(--spacing-4);
    margin-bottom: var(--spacing-12);
}

.stats-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: var(--spacing-5);
    max-width: 900px;
    margin: 0 auto;
}

/* Pricing */
.pricing-section {
    background: var(--bg-primary);
}

.pricing-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: var(--spacing-6);
}

.price-card .btn {
    margin-top: var(--spacing-6);
}

/* Features */
.features-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: var(--spacing-8);
}

.feature-card {
    text-align: left;
}

.feature-card h4 {
    margin-bottom: var(--spacing-4);
}

/* Scenarios */
.scenarios-grid {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: var(--spacing-6);
}

/* CTA */
.cta-section {
    background: linear-gradient(135deg, var(--primary-600) 0%, var(--primary-700) 100%);
    padding: var(--spacing-16) 0;
}

.cta-content {
    text-align: center;
}

.cta-content h2 {
    color: var(--text-inverse);
    margin-bottom: var(--spacing-4);
}

.cta-content p {
    color: rgba(255, 255, 255, 0.8);
    font-size: var(--font-size-lg);
    margin-bottom: var(--spacing-8);
}

.cta-actions {
    display: flex;
    justify-content: center;
    gap: var(--spacing-4);
}

/* Footer */
.footer {
    background: var(--bg-dark);
    color: var(--text-inverse);
    padding: var(--spacing-16) 0 var(--spacing-8);
}

.footer-grid {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr 1fr;
    gap: var(--spacing-10);
    margin-bottom: var(--spacing-12);
}

.footer-brand .logo {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-xl);
    font-weight: var(--font-weight-bold);
    color: var(--text-inverse);
    margin-bottom: var(--spacing-4);
}

.footer-brand p {
    color: var(--gray-400);
    font-size: var(--font-size-sm);
}

.footer-links h5 {
    color: var(--text-inverse);
    font-size: var(--font-size-sm);
    margin-bottom: var(--spacing-4);
}

.footer-links a {
    display: block;
    color: var(--gray-400);
    font-size: var(--font-size-sm);
    padding: var(--spacing-1) 0;
}

.footer-links a:hover {
    color: var(--text-inverse);
}

.footer-bottom {
    border-top: 1px solid var(--gray-700);
    padding-top: var(--spacing-6);
    text-align: center;
}

.footer-bottom p {
    color: var(--gray-500);
    font-size: var(--font-size-sm);
    margin: 0;
}

/* Responsive */
@media (max-width: 1024px) {
    .pricing-grid,
    .features-grid,
    .scenarios-grid {
        grid-template-columns: repeat(2, 1fr);
    }

    .stats-grid {
        grid-template-columns: repeat(2, 1fr);
    }

    .footer-grid {
        grid-template-columns: repeat(2, 1fr);
    }
}

@media (max-width: 768px) {
    .hero-title {
        font-size: var(--font-size-4xl);
    }

    .hero-actions {
        flex-direction: column;
        align-items: center;
    }

    .pricing-grid,
    .features-grid,
    .scenarios-grid,
    .stats-grid {
        grid-template-columns: 1fr;
    }

    .footer-grid {
        grid-template-columns: 1fr;
        text-align: center;
    }

    .footer-brand .logo {
        justify-content: center;
    }

    .cta-actions {
        flex-direction: column;
        align-items: center;
    }
}
</style>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '../../stores/auth'
import Navbar from '../../components/Navbar.vue'
import { CheckCircle, CreditCard, Loader2 } from 'lucide-vue-next'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const productId = route.query.product as string
const status = ref<'confirm' | 'processing' | 'success'>('confirm')

onMounted(() => {
    if (!auth.user) {
        router.push('/login')
    }
})

const handleConfirmPayment = async () => {
    status.value = 'processing'
    // TODO: Integrate actual payment gateway API (Stripe, etc.)
    await new Promise(resolve => setTimeout(resolve, 2000))
    status.value = 'success'
    // Redirect to dashboard after success
    setTimeout(() => router.push('/dashboard/instances'), 2000)
}
</script>

<template>
  <div class="payment-page">
    <Navbar />
    <div class="payment-container">
        <div class="card payment-card">
        <template v-if="status === 'confirm'">
            <h2 class="title">Complete Purchase</h2>
            <div class="order-summary">
                <div class="label">Product</div>
                <div class="value">{{ productId }}</div>
                <hr class="divider" />
                <div class="total-row">
                    <span>Total Due Today</span>
                    <span class="total-amount">$0.00 (Trial)</span>
                </div>
            </div>
            
            <button class="btn btn-primary btn-block btn-lg" @click="handleConfirmPayment">
                <CreditCard :size="20" class="btn-icon" /> Conform & Pay
            </button>
        </template>

        <template v-if="status === 'processing'">
            <div class="status-view">
                <div class="spinner">
                    <Loader2 :size="48" color="var(--primary-color)" />
                </div>
                <h3>Processing Payment...</h3>
                <p>Please do not close this window.</p>
            </div>
        </template>

        <template v-if="status === 'success'">
            <div class="status-view">
                <div class="success-icon">
                    <CheckCircle :size="64" />
                </div>
                <h3>Payment Successful!</h3>
                <p>Redirecting to your dashboard...</p>
            </div>
        </template>
        </div>
    </div>
  </div>
</template>

<style scoped>
.payment-page {
  min-height: 100vh;
  background-color: #f1f5f9;
}

.payment-container {
  min-height: calc(100vh - 80px);
  display: flex;
  align-items: center;
  justify-content: center;
}

.payment-card {
  width: 100%;
  max-width: 500px;
  padding: 40px;
  text-align: center;
}

.title {
  margin-bottom: 24px;
}

.order-summary {
  background-color: #f8fafc;
  padding: 24px;
  border-radius: 12px;
  margin-bottom: 24px;
  text-align: left;
}

.label {
  margin-bottom: 8px;
  color: var(--text-secondary);
}

.value {
  font-size: 1.25rem;
  font-weight: bold;
}

.divider {
  border: none;
  border-top: 1px solid #e2e8f0;
  margin: 16px 0;
}

.total-row {
  display: flex;
  justify-content: space-between;
}

.total-amount {
  font-weight: bold;
}

.btn-block {
  width: 100%;
}

.btn-lg {
  font-size: 1.1rem;
  padding: 16px;
}

.btn-icon {
  margin-right: 8px;
}

.status-view {
  padding: 40px 0;
}

.spinner {
  display: inline-block;
  margin-bottom: 24px;
  animation: spin 1s linear infinite;
}

@keyframes spin { 
    0% { transform: rotate(0deg); } 
    100% { transform: rotate(360deg); } 
}

.success-icon {
    color: #16a34a;
    margin-bottom: 24px;
    display: flex;
    justify-content: center; /* flex center helpful for icon container */
}

h3 {
    margin-bottom: 16px;
    font-size: 1.5rem;
    font-weight: bold;
}

p {
    color: var(--text-secondary);
}
</style>

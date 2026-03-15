<script setup lang="ts">
import { ref, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldCheck, RefreshCw, CheckCircle2 } from 'lucide-vue-next'

const { t } = useI18n()
const emit = defineEmits(['verify'])

const userInput = ref('')
const captchaCode = ref('')
const isVerified = ref(false)
const showError = ref(false)
const canvasRef = ref<HTMLCanvasElement | null>(null)

const generateCaptcha = () => {
    const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789'
    let code = ''
    for (let i = 0; i < 4; i++) {
        code += chars.charAt(Math.floor(Math.random() * chars.length))
    }
    captchaCode.value = code
    drawCaptcha(code)
    userInput.value = ''
    showError.value = false
    isVerified.value = false
}

const drawCaptcha = (code: string) => {
    const canvas = canvasRef.value
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    ctx.clearRect(0, 0, canvas.width, canvas.height)
    
    // Background with slight gradient
    const gradient = ctx.createLinearGradient(0, 0, canvas.width, canvas.height)
    gradient.addColorStop(0, '#f8fafc')
    gradient.addColorStop(1, '#f1f5f9')
    ctx.fillStyle = gradient
    ctx.fillRect(0, 0, canvas.width, canvas.height)

    // Noise: Lines
    for (let i = 0; i < 5; i++) {
        ctx.strokeStyle = `rgba(${Math.random() * 255},${Math.random() * 255},${Math.random() * 255}, 0.2)`
        ctx.beginPath()
        ctx.moveTo(Math.random() * canvas.width, Math.random() * canvas.height)
        ctx.lineTo(Math.random() * canvas.width, Math.random() * canvas.height)
        ctx.stroke()
    }

    // Noise: Dots
    for (let i = 0; i < 30; i++) {
        ctx.fillStyle = `rgba(${Math.random() * 255},${Math.random() * 255},${Math.random() * 255}, 0.2)`
        ctx.beginPath()
        ctx.arc(Math.random() * canvas.width, Math.random() * canvas.height, 1, 0, 2 * Math.PI)
        ctx.fill()
    }

    // Characters
    const fontSize = 28
    ctx.font = `bold ${fontSize}px "Outfit", system-ui, sans-serif`
    ctx.textBaseline = 'middle'

    for (let i = 0; i < code.length; i++) {
        const char = code[i]
        ctx.save()
        
        // Random placement and rotation
        const x = 20 + i * 25
        const y = canvas.height / 2 + (Math.random() * 10 - 5)
        const angle = (Math.random() * 40 - 20) * Math.PI / 180
        
        ctx.translate(x, y)
        ctx.rotate(angle)
        
        // Random color
        const colors = ['#0f172a', '#1e293b', '#334155', '#475569', '#2563eb', '#7c3aed', '#db2777']
        ctx.fillStyle = colors[Math.floor(Math.random() * colors.length)]
        
        ctx.fillText(char, 0, 0)
        ctx.restore()
    }
}

const handleVerify = () => {
    if (userInput.value.toLowerCase() === captchaCode.value.toLowerCase()) {
        isVerified.value = true
        showError.value = false
        emit('verify')
    } else {
        showError.value = true
        // Optional: refresh on error
        setTimeout(() => {
            generateCaptcha()
        }, 800)
    }
}

// Auto-check if 4 characters entered
watch(userInput, (val) => {
    if (val.length === 4) {
        handleVerify()
    } else if (val.length > 0) {
        showError.value = false
    }
})

onMounted(() => {
    generateCaptcha()
})
</script>

<template>
    <div class="security-verify">
        <div class="verify-header">
            <ShieldCheck :size="16" class="header-icon" />
            <span>{{ t('auth.securityCheck') }}</span>
        </div>
        
        <div class="captcha-container" :class="{ 'is-verified': isVerified, 'has-error': showError }">
            <div class="captcha-visual">
                <canvas 
                    ref="canvasRef" 
                    width="130" 
                    height="48" 
                    class="captcha-canvas"
                    @click="generateCaptcha"
                ></canvas>
                <button 
                    type="button" 
                    class="refresh-btn" 
                    @click="generateCaptcha" 
                    :title="t('actions.refresh')"
                    v-if="!isVerified"
                >
                    <RefreshCw :size="18" />
                </button>
            </div>
            
            <div class="input-section">
                <input 
                    type="text" 
                    v-model="userInput" 
                    :placeholder="t('auth.captchaPlaceholder')"
                    class="captcha-input"
                    maxlength="4"
                    :disabled="isVerified"
                    autocapitalize="off"
                    autocorrect="off"
                    spellcheck="false"
                />
                <div v-if="isVerified" class="success-mark">
                    <CheckCircle2 :size="20" />
                </div>
            </div>
        </div>
        
        <p v-if="showError" class="error-text">
            {{ t('auth.incorrectCaptcha') }}
        </p>
    </div>
</template>

<style scoped>
.security-verify {
    margin-bottom: var(--spacing-6);
    user-select: none;
    animation: fadeInScale 0.4s cubic-bezier(0.16, 1, 0.3, 1);
}

@keyframes fadeInScale {
    from { opacity: 0; transform: scale(0.95); }
    to { opacity: 1; transform: scale(1); }
}

.verify-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-2);
    font-size: var(--font-size-xs);
    font-weight: 600;
    color: var(--text-secondary);
    margin-bottom: var(--spacing-2);
    text-transform: uppercase;
    letter-spacing: 0.05em;
}

.header-icon {
    color: var(--primary-500);
}

.captcha-container {
    display: flex;
    gap: var(--spacing-3);
    align-items: stretch;
}

.captcha-visual {
    position: relative;
    display: flex;
    align-items: center;
}

.captcha-canvas {
    border-radius: var(--radius-lg);
    border: 1px solid var(--border-light);
    cursor: pointer;
    transition: transform 0.2s;
    background: white;
}

.captcha-canvas:hover {
    transform: translateY(-1px);
    box-shadow: var(--shadow-sm);
}

.refresh-btn {
    position: absolute;
    right: -10px;
    top: -10px;
    width: 24px;
    height: 24px;
    background: white;
    border: 1px solid var(--border-light);
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--text-tertiary);
    cursor: pointer;
    box-shadow: var(--shadow-sm);
    transition: all 0.2s;
}

.refresh-btn:hover {
    color: var(--primary-600);
    transform: rotate(90deg);
}

.input-section {
    flex: 1;
    position: relative;
}

.captcha-input {
    width: 100%;
    height: 100%;
    padding: 10px 14px;
    border: 1px solid var(--border-light);
    border-radius: var(--radius-lg);
    font-size: var(--font-size-lg);
    font-weight: 700;
    letter-spacing: 0.2em;
    text-align: center;
    text-transform: uppercase;
    transition: all 0.2s;
    background: white;
}

.captcha-input:focus {
    outline: none;
    border-color: var(--primary-400);
    box-shadow: 0 0 0 4px var(--primary-50);
}

.has-error .captcha-input {
    border-color: var(--error-400);
    background-color: var(--error-50);
    color: var(--error-700);
    animation: shake 0.5s cubic-bezier(.36,.07,.19,.97) both;
}

@keyframes shake {
    10%, 90% { transform: translate3d(-1px, 0, 0); }
    20%, 80% { transform: translate3d(2px, 0, 0); }
    30%, 50%, 70% { transform: translate3d(-4px, 0, 0); }
    40%, 60% { transform: translate3d(4px, 0, 0); }
}

.is-verified .captcha-input {
    border-color: var(--success-400, #4ade80);
    background-color: var(--success-50, #f0fdf4);
    color: var(--success-700, #15803d);
}

.success-mark {
    position: absolute;
    right: 12px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--success-500);
    animation: popIn 0.3s cubic-bezier(0.34, 1.56, 0.64, 1);
}

@keyframes popIn {
    from { transform: translateY(-50%) scale(0); }
    to { transform: translateY(-50%) scale(1); }
}

.error-text {
    margin-top: var(--spacing-2);
    font-size: var(--font-size-xs);
    color: var(--error-600);
    font-weight: 600;
}
</style>

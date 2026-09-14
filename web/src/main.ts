import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import i18n from './locales'
import './index.css'
import { initTracing, traceRouter } from './tracing'

// 在发出任何 API 请求前注册 XHR 追踪
initTracing()
traceRouter(router)

const app = createApp(App)

app.use(createPinia())
app.use(router)
app.use(i18n)

app.mount('#root')


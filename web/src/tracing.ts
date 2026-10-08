/**
 * 浏览器端链路追踪：
 * - XHR 请求（axios）创建 span 并注入 W3C traceparent，与网关、clapi 等后端链路串成一条 trace
 * - 页面加载、路由切换、VNC 连接建立的耗时
 * span 经网关的登录态接口转发到链路追踪后端。
 */
import { SpanStatusCode, trace } from '@opentelemetry/api'
import type { Span } from '@opentelemetry/api'
import { ExportResultCode } from '@opentelemetry/core'
import type { ExportResult } from '@opentelemetry/core'
import { registerInstrumentations } from '@opentelemetry/instrumentation'
import { XMLHttpRequestInstrumentation } from '@opentelemetry/instrumentation-xml-http-request'
import { JsonTraceSerializer } from '@opentelemetry/otlp-transformer'
import { resourceFromAttributes } from '@opentelemetry/resources'
import {
    BatchSpanProcessor,
    ParentBasedSampler,
    TraceIdRatioBasedSampler,
    WebTracerProvider,
} from '@opentelemetry/sdk-trace-web'
import type { ReadableSpan, SpanExporter } from '@opentelemetry/sdk-trace-web'
import type { Router } from 'vue-router'
import { getToken } from './api/client'

const EXPORT_URL = '/api/v1/telemetry/traces'
// keepalive 请求体上限 64KB，超过时改用普通请求
const KEEPALIVE_LIMIT = 60 * 1024

let tracingEnabled = false

const tracer = () => trace.getTracer('cloudland-web')

/**
 * 通过网关登录态接口上报 span；未登录时直接丢弃。
 * 使用 fetch 而非 axios：避免 401 触发全局退出登录，也避免 XHR 插件追踪上报请求本身
 */
class GatewaySpanExporter implements SpanExporter {
    export(spans: ReadableSpan[], resultCallback: (result: ExportResult) => void): void {
        const token = getToken()
        const body = JsonTraceSerializer.serializeRequest(spans)
        if (!token || !body) {
            resultCallback({ code: ExportResultCode.SUCCESS })
            return
        }
        fetch(EXPORT_URL, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
            body,
            keepalive: body.byteLength < KEEPALIVE_LIMIT,
        })
            .then(() => resultCallback({ code: ExportResultCode.SUCCESS }))
            .catch(() => resultCallback({ code: ExportResultCode.FAILED }))
    }

    shutdown(): Promise<void> {
        return Promise.resolve()
    }

    forceFlush(): Promise<void> {
        return Promise.resolve()
    }
}

/** 初始化浏览器链路追踪；VITE_TRACING_ENABLED=false 时关闭，VITE_TRACING_SAMPLE_RATIO 控制采样率 */
export function initTracing(): void {
    if (import.meta.env.VITE_TRACING_ENABLED === 'false') {
        return
    }
    const ratio = Number(import.meta.env.VITE_TRACING_SAMPLE_RATIO ?? '1')
    const provider = new WebTracerProvider({
        resource: resourceFromAttributes({ 'service.name': 'cloudland-web' }),
        sampler: new ParentBasedSampler({
            root: new TraceIdRatioBasedSampler(Number.isFinite(ratio) ? ratio : 1),
        }),
        spanProcessors: [new BatchSpanProcessor(new GatewaySpanExporter())],
    })
    provider.register()

    registerInstrumentations({
        instrumentations: [
            // 默认只向同源请求注入 traceparent，不会外发到第三方地址
            new XMLHttpRequestInstrumentation({ ignoreUrls: [EXPORT_URL] }),
        ],
    })
    tracingEnabled = true
    tracePageLoad()
}

/** 页面加载耗时：在 load 事件之后读取 Navigation Timing 补记一个 span */
function tracePageLoad(): void {
    const record = () => {
        const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined
        if (!nav || nav.loadEventEnd === 0) {
            return
        }
        const origin = performance.timeOrigin
        const span = tracer().startSpan('page load', {
            startTime: origin + nav.startTime,
            attributes: {
                'url.path': window.location.pathname,
                'page.ttfb_ms': Math.round(nav.responseStart - nav.startTime),
                'page.dom_content_loaded_ms': Math.round(nav.domContentLoadedEventEnd - nav.startTime),
                'page.transfer_bytes': nav.transferSize,
            },
        })
        span.end(origin + nav.loadEventEnd)
    }
    const schedule = () => setTimeout(record, 0)
    if (document.readyState === 'complete') {
        schedule()
    } else {
        window.addEventListener('load', schedule, { once: true })
    }
}

/** 路由切换耗时：span 以路由模板命名（不含资源 ID），避免名称基数过高 */
export function traceRouter(router: Router): void {
    if (!tracingEnabled) {
        return
    }
    let pending: Span | undefined
    const finish = (error?: unknown) => {
        if (error) {
            pending?.setStatus({ code: SpanStatusCode.ERROR, message: String(error) })
        }
        pending?.end()
        pending = undefined
    }
    router.beforeEach((to) => {
        finish()
        const route = to.matched[to.matched.length - 1]?.path ?? to.path
        pending = tracer().startSpan(`route ${route}`, {
            attributes: { 'route.name': String(to.name ?? '') },
        })
    })
    router.afterEach((_to, _from, failure) => finish(failure))
    router.onError((error) => finish(error))
}

/** VNC WebSocket 无法携带 trace 头：记录从创建 RFB 到连接成功（或失败）的耗时 */
export function traceVncConnection(rfb: EventTarget): void {
    if (!tracingEnabled) {
        return
    }
    const span = tracer().startSpan('vnc connect')
    let ended = false
    const end = (error?: string) => {
        if (ended) {
            return
        }
        ended = true
        if (error) {
            span.setStatus({ code: SpanStatusCode.ERROR, message: error })
        }
        span.end()
    }
    rfb.addEventListener('connect', () => end(), { once: true })
    rfb.addEventListener('disconnect', () => end('disconnected before connect'), { once: true })
    rfb.addEventListener('securityfailure', () => end('security failure'), { once: true })
}

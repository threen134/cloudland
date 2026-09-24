/// <reference types="vite/client" />

declare module '*.vue' {
    import type { DefineComponent } from 'vue'
    const component: DefineComponent<object, object, any>
    export default component
}

interface ImportMetaEnv {
    /** 设为 'false' 关闭浏览器链路追踪 */
    readonly VITE_TRACING_ENABLED?: string
    /** 根 span 采样率（0~1），默认 1 */
    readonly VITE_TRACING_SAMPLE_RATIO?: string
}

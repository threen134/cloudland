// @novnc/novnc 1.6.0 只发布 JS，没有类型声明（package.json 也没有 types 字段），
// 这里按 InstanceConsole.vue 实际用到的接口补一份最小声明。
// 升级该依赖后若上游自带了类型，删掉这个文件即可。

declare module '@novnc/novnc/lib/rfb' {
    export interface RFBOptions {
        credentials?: { username?: string; password?: string; target?: string }
        shared?: boolean
        repeaterID?: string
        wsProtocols?: string[]
    }

    export default class RFB extends EventTarget {
        constructor(target: Element, urlOrChannel: string | WebSocket, options?: RFBOptions)
        viewOnly: boolean
        focusOnClick: boolean
        clipViewport: boolean
        dragViewport: boolean
        scaleViewport: boolean
        resizeSession: boolean
        showDotCursor: boolean
        background: string
        qualityLevel: number
        compressionLevel: number
        disconnect(): void
        focus(options?: FocusOptions): void
        blur(): void
        machineShutdown(): void
        machineReboot(): void
        machineReset(): void
        sendCredentials(credentials: RFBOptions['credentials']): void
        // code 传 null 表示只发 keysym，由虚拟机侧按自身键盘布局映射
        sendKey(keysym: number, code: string | null, down?: boolean): void
        sendCtrlAltDel(): void
        clipboardPasteFrom(text: string): void
    }
}

declare module '@novnc/novnc/lib/input/keysym' {
    // X11 keysym 常量表（XK_Return、XK_F1 …）
    const KeyTable: Record<string, number>
    export default KeyTable
}

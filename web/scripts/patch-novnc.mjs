// @novnc/novnc 在模块顶层用了 top-level await，Rollup 打包会直接报错。
// 原先这个补丁只写在 deploy/docker/dockerfiles/Dockerfile.nginx-ui 里，
// 于是本地和 CI 跑 npm run build 都会失败。改为 postinstall 自动处理。
//
// 升级 @novnc/novnc 后如果这里匹配不到，先确认新版本是否已经修复，再决定是否删掉本脚本。

import fs from 'node:fs'
import path from 'node:path'
import url from 'node:url'

const root = path.resolve(path.dirname(url.fileURLToPath(import.meta.url)), '..')
const target = path.join(root, 'node_modules/@novnc/novnc/lib/util/browser.js')
const NEEDLE = '= await _checkWebCodecsH264DecodeSupport();'
const REPLACEMENT = '= false;'

if (!fs.existsSync(target)) {
    // 依赖还没装好（例如只装了生产依赖），不是错误
    process.exit(0)
}

const source = fs.readFileSync(target, 'utf8')
if (!source.includes(NEEDLE)) {
    // 已经打过补丁，或上游改了实现
    process.exit(0)
}

fs.writeFileSync(target, source.replace(NEEDLE, REPLACEMENT))
console.log('patched @novnc/novnc: 去掉顶层 await，Rollup 才能打包')

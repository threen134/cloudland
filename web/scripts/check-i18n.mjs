// 校验三个语言包与代码里实际用到的文案键是否一致。
//
// 起因：`marketplace.cancel` 这个键只有英文包里有，中文界面一直静默回退成英文；
// 后来它被当作无用残留删掉，创建虚拟机页面就直接显示出了键名 "marketplace.cancel"。
// vue-i18n 找不到键时返回键名本身（是个真值），所以既不会报错，`t('a') || t('b')`
// 这种回退写法也永远不会生效——只能靠校验发现。
//
// 检查三件事：
//   1. 三个语言包的键是否完全一致（以 en 为基准）
//   2. 代码里 t('x.y') 用到的键在语言包里是否存在
//   3. 语言包里是否有谁都不用的键（只提示，不失败：很多键是运行时拼出来的）
//
// 用法：npm run i18n:check

import fs from 'node:fs'
import path from 'node:path'
import url from 'node:url'
import ts from 'typescript'

const root = path.resolve(path.dirname(url.fileURLToPath(import.meta.url)), '..')
const srcDir = path.join(root, 'src')
const localesDir = path.join(srcDir, 'locales')
const LOCALES = ['en', 'zh', 'zh-TW']
const BASE = 'en'

// 语言包是 TS 文件，先转成 JS 再动态导入，避免自己写解析器出错
const loadLocale = async (name) => {
    const source = fs.readFileSync(path.join(localesDir, `${name}.ts`), 'utf8')
    const { outputText } = ts.transpileModule(source, {
        compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ESNext },
    })
    const mod = await import('data:text/javascript;charset=utf-8,' + encodeURIComponent(outputText))
    return mod.default
}

const flatten = (obj, prefix = '', out = new Set()) => {
    for (const [key, value] of Object.entries(obj)) {
        const full = prefix ? `${prefix}.${key}` : key
        if (value && typeof value === 'object' && !Array.isArray(value)) {
            flatten(value, full, out)
        } else {
            out.add(full)
        }
    }
    return out
}

const walk = (dir, files = []) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name)
        if (entry.isDirectory()) walk(full, files)
        else if (/\.(vue|ts)$/.test(entry.name) && !full.startsWith(localesDir)) files.push(full)
    }
    return files
}

// t('a.b') / $t("a.b") / te('a.b')。前置的 (?<![\w$]) 是为了排掉 emit('close')、
// getContext('2d') 这类以 t( 结尾的调用
const STATIC_KEY = /(?<![\w$])\$?te?\(\s*['"]([A-Za-z0-9_][A-Za-z0-9_.]*)['"]/g
// t('a.b.' + x) / t(`a.b.${x}`)：键是拼出来的，只能记住前缀，用于判断"未使用"时放行
const DYNAMIC_PREFIX = /(?<![\w$])\$?te?\(\s*[`'"]([A-Za-z0-9_][A-Za-z0-9_.]*\.)(?:['"]\s*\+|\$\{)/g

const collectUsage = (files) => {
    const used = new Map() // key -> [位置]
    const dynamicPrefixes = new Set()
    for (const file of files) {
        const text = fs.readFileSync(file, 'utf8')
        const lines = text.split('\n')
        lines.forEach((line, i) => {
            for (const m of line.matchAll(STATIC_KEY)) {
                // 以点结尾的是 t('前缀.' + 变量) 这种拼接，只是前缀不是完整键
                if (m[1].endsWith('.')) {
                    dynamicPrefixes.add(m[1])
                    continue
                }
                const rel = path.relative(root, file).replace(/\\/g, '/')
                if (!used.has(m[1])) used.set(m[1], [])
                used.get(m[1]).push(`${rel}:${i + 1}`)
            }
            for (const m of line.matchAll(DYNAMIC_PREFIX)) dynamicPrefixes.add(m[1])
        })
    }
    return { used, dynamicPrefixes }
}

const main = async () => {
    const localeKeys = {}
    for (const name of LOCALES) localeKeys[name] = flatten(await loadLocale(name))

    const problems = []
    const baseKeys = localeKeys[BASE]

    // 1. 语言包之间对齐
    for (const name of LOCALES) {
        if (name === BASE) continue
        const missing = [...baseKeys].filter((k) => !localeKeys[name].has(k))
        const extra = [...localeKeys[name]].filter((k) => !baseKeys.has(k))
        if (missing.length) problems.push(`${name}.ts 缺少 ${missing.length} 个键：\n  ${missing.join('\n  ')}`)
        if (extra.length) problems.push(`${name}.ts 多出 ${extra.length} 个 ${BASE}.ts 没有的键：\n  ${extra.join('\n  ')}`)
    }

    // 2. 代码用到但语言包没有
    const { used, dynamicPrefixes } = collectUsage(walk(srcDir))
    const undefinedKeys = [...used.keys()].filter((k) => k.includes('.') && !baseKeys.has(k))
    if (undefinedKeys.length) {
        problems.push(
            `代码里用到但 ${BASE}.ts 没有的键（界面会直接显示键名）：\n` +
                undefinedKeys.map((k) => `  ${k}  ← ${used.get(k).join(', ')}`).join('\n')
        )
    }

    // 3. 没人用的键：只提示。运行时拼出来的键（如 dashboard.instanceStatus.<状态>）无法静态识别
    const unused = [...baseKeys].filter(
        (k) => !used.has(k) && ![...dynamicPrefixes].some((p) => k.startsWith(p))
    )

    if (problems.length) {
        console.error('i18n 校验失败：\n')
        for (const p of problems) console.error(p + '\n')
        process.exit(1)
    }

    console.log(`i18n 校验通过：${LOCALES.join(' / ')} 各 ${baseKeys.size} 个键，代码用到的 ${used.size} 个键均存在。`)
    if (unused.length) {
        console.log(`提示：${unused.length} 个键未被静态引用（可能是运行时拼接的键，删除前务必先确认引用）。`)
    }
}

main().catch((err) => {
    console.error(err)
    process.exit(1)
})

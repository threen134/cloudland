// 校验三个语言包与代码里实际用到的文案键是否一致。
//
// 起因：`marketplace.cancel` 这个键只有英文包里有，中文界面一直静默回退成英文；
// 后来它被当作无用残留删掉，创建虚拟机页面就直接显示出了键名 "marketplace.cancel"。
// vue-i18n 找不到键时返回键名本身（是个真值），所以既不会报错，`t('a') || t('b')`
// 这种回退写法也永远不会生效——只能靠校验发现。
//
// 检查四件事：
//   1. 三个语言包的键是否完全一致（以 en 为基准）
//   2. 代码里 t('x.y') 用到的键在语言包里是否存在
//   3. 语言包里是否有谁都不用的键（只提示，不失败：很多键是运行时拼出来的）
//   4. 模板里有没有本该走 i18n 却写死的文案（支付页、激活页那批以前是靠人眼扫出来的）
//
// 用法：npm run i18n:check
//
// 误报处理：确实不该翻译的（品牌名、单位、示例值）加到下面的 ALLOW_* 里；
// 个别位置在那一行写 `i18n-ignore` 注释即可跳过。

import fs from 'node:fs'
import path from 'node:path'
import url from 'node:url'
import ts from 'typescript'
import { parse as parseSFC } from 'vue/compiler-sfc'

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

// ---------- hardcoded user-visible text ----------

// CJK in template text or in a code string literal is almost certainly hardcoded copy
const CJK = /[㐀-䶿一-鿿぀-ヿ가-힯]/

// Text inside these tags is not prose
const SKIP_TAGS = new Set(['style', 'script', 'code', 'pre'])
// Attributes whose static value is shown to the user
const TEXT_ATTRS = new Set(['placeholder', 'title', 'alt', 'aria-label', 'label', 'confirm-text'])

// Technical words: units, protocols, terms and key names, not copy on their own
const TECH_WORDS = new Set([
    'cpu', 'vcpu', 'vcpus', 'ram', 'gb', 'mb', 'kb', 'tb', 'ghz', 'mhz', 'ms', 'iops',
    'mbps', 'kbps', 'gbps', 'ip', 'ips', 'ipv', 'mac', 'cidr', 'dns', 'mtu', 'vlan', 'vxlan',
    'vni', 'vpc', 'vnc', 'ssh', 'rdp', 'sftp', 'scp', 'api', 'url', 'uri', 'uuid', 'id', 'ids',
    'http', 'https', 'tcp', 'udp', 'icmp', 'json', 'yaml', 'sql', 'html', 'css', 'svg',
    'qcow', 'raw', 'iso', 'kvm', 'vm', 'os', 'ok', 'utc', 'gmt', 'md', 'sha', 'webhook',
    'ctrl', 'alt', 'del', 'esc', 'tab', 'shift', 'enter', 'backspace', 'space',
])

// Allowed when the whole string matches: brand names, symbols, terms left untranslated
const ALLOW_EXACT = new Set([
    'CloudLand', 'Trace ID:', 'N/A', '···', '••••••••',
])

// Allowed when the whole string matches: symbols and digits only, sample values, snippets
const ALLOW_PATTERNS = [
    /^[^\p{L}]*$/u,                       // no letter at all: digits, symbols, whitespace
    /^[a-z0-9_.-]+@[a-z0-9.-]+$/i,        // sample email address
    /^\{.*\}$/s,                          // sample JSON
]

const isHardcodedText = (raw) => {
    const text = raw.replace(/\s+/g, ' ').trim()
    if (!text) return false
    if (ALLOW_EXACT.has(text)) return false
    if (ALLOW_PATTERNS.some((re) => re.test(text))) return false
    if (CJK.test(text)) return true
    // English: keep only plain words — drop identifiers (digits or underscores, e.g. kvm-x86_64,
    // Base64), all-caps abbreviations (ARM, URL) and technical words. Two or more words left,
    // or a single word of 4+ letters, counts as copy
    const words = (text.match(/[A-Za-z][A-Za-z0-9_'-]*/g) || []).filter(
        (w) => !/[0-9_]/.test(w) && !/^[A-Z]+$/.test(w) && !TECH_WORDS.has(w.toLowerCase())
    )
    return words.length >= 2 || words.some((w) => w.length >= 4)
}

// Template AST node types (@vue/compiler-core NodeTypes)
const NODE_ELEMENT = 1
const NODE_TEXT = 2
const NODE_ATTRIBUTE = 6

const scanTemplate = (ast, rel, lines, hits) => {
    const found = []
    const visit = (node) => {
        if (node.type === NODE_TEXT) {
            if (isHardcodedText(node.content)) {
                found.push({ where: `${rel}:${node.loc.start.line}`, text: node.content.trim(), kind: '模板文本' })
            }
            return
        }
        if (node.type === NODE_ELEMENT) {
            if (SKIP_TAGS.has(node.tag)) return
            for (const prop of node.props || []) {
                if (prop.type !== NODE_ATTRIBUTE || !prop.value) continue
                if (!TEXT_ATTRS.has(prop.name)) continue
                if (isHardcodedText(prop.value.content)) {
                    found.push({
                        where: `${rel}:${prop.loc.start.line}`,
                        text: `${prop.name}="${prop.value.content}"`,
                        kind: '模板属性',
                    })
                }
            }
        }
        for (const child of node.children || []) visit(child)
    }
    visit(ast)
    // skip anything on a line marked i18n-ignore
    for (const hit of found) {
        const line = Number(hit.where.split(':').pop())
        if (!(lines[line - 1] || '').includes('i18n-ignore')) hits.push(hit)
    }
}

// CJK string literals in code. Parsed with TypeScript rather than matched with a regex:
// Chinese comments are everywhere, and a regex cannot tell a comment from a string
const scanScript = (code, rel, lineOffset, hits) => {
    const sf = ts.createSourceFile(rel, code, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
    const visit = (node) => {
        if (
            (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isTemplateHead(node) ||
                ts.isTemplateMiddle(node) || ts.isTemplateTail(node)) &&
            CJK.test(node.text)
        ) {
            const { line } = sf.getLineAndCharacterOfPosition(node.getStart())
            const at = line + lineOffset + 1
            if (!(code.split('\n')[line] || '').includes('i18n-ignore')) {
                hits.push({ where: `${rel}:${at}`, text: node.text.trim(), kind: '代码字符串' })
            }
        }
        ts.forEachChild(node, visit)
    }
    visit(sf)
    return hits
}

const collectHardcoded = (files) => {
    const hits = []
    for (const file of files) {
        const rel = path.relative(root, file).replace(/\\/g, '/')
        const text = fs.readFileSync(file, 'utf8')
        if (file.endsWith('.vue')) {
            const { descriptor, errors } = parseSFC(text, { filename: rel })
            if (errors.length) continue
            const lines = text.split('\n')
            if (descriptor.template?.ast) scanTemplate(descriptor.template.ast, rel, lines, hits)
            for (const block of [descriptor.script, descriptor.scriptSetup]) {
                if (block) scanScript(block.content, rel, block.loc.start.line - 1, hits)
            }
        } else {
            scanScript(text, rel, 0, hits)
        }
    }
    return hits
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
    const files = walk(srcDir)
    const { used, dynamicPrefixes } = collectUsage(files)
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

    // 4. 写死的文案
    const hardcoded = collectHardcoded(files)
    if (hardcoded.length) {
        problems.push(
            `${hardcoded.length} 处写死的文案（应改用 t('...')；确实不需要翻译的加进脚本里的 ALLOW_* 或在该行写 i18n-ignore）：\n` +
                hardcoded.map((h) => `  ${h.where}  [${h.kind}] ${JSON.stringify(h.text)}`).join('\n')
        )
    }

    if (problems.length) {
        console.error('i18n 校验失败：\n')
        for (const p of problems) console.error(p + '\n')
        process.exit(1)
    }

    console.log(`i18n 校验通过：${LOCALES.join(' / ')} 各 ${baseKeys.size} 个键，代码用到的 ${used.size} 个键均存在，未发现写死的文案。`)
    if (unused.length) {
        console.log(`提示：${unused.length} 个键未被静态引用（可能是运行时拼接的键，删除前务必先确认引用）。`)
    }
}

main().catch((err) => {
    console.error(err)
    process.exit(1)
})

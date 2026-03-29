import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'
import { generateSidebar } from 'vitepress-sidebar'

export default withMermaid(
  defineConfig({
    base: '/docs',
    title: 'CloudLand',
    description: '轻量级 IaaS 云平台 — 文档中心',
    lang: 'zh-CN',
    lastUpdated: true,

    markdown: {
      // Mermaid configuration is handled by withMermaid
    },

    head: [
      ['link', { rel: 'icon', type: 'image/svg+xml', href: '/logo.svg' }],
      ['meta', { name: 'theme-color', content: '#3b82f6' }],
    ],

    themeConfig: {
      logo: '/logo.svg',
      siteTitle: 'CloudLand',

      nav: [
        { text: '首页', link: '/' },
        { text: '指南', link: '/guide/introduction' },
        { text: 'API 参考', link: '/api/overview' },
        { text: '架构', link: '/architecture/overview' },
        { text: '部署', link: '/deployment/index' },
      ],

      sidebar: generateSidebar([
        {
          documentRootPath: '.',
          scanStartPath: 'guide',
          resolvePath: '/guide/',
          useTitleFromFileHeading: true,
          includeRootIndexFile: true,
          collapsed: true,
          useFolderTitleFromIndexFile: true,
          useFolderLinkFromIndexFile: true
        },
        {
          documentRootPath: '.',
          scanStartPath: 'api',
          resolvePath: '/api/',
          useTitleFromFileHeading: true,
          includeRootIndexFile: true,
          collapsed: true,
          useFolderTitleFromIndexFile: true,
          useFolderLinkFromIndexFile: true
        },
        {
          documentRootPath: '.',
          scanStartPath: 'architecture',
          resolvePath: '/architecture/',
          useTitleFromFileHeading: true,
          includeRootIndexFile: true,
          collapsed: true,
          useFolderTitleFromIndexFile: true,
          useFolderLinkFromIndexFile: true
        },
        {
          documentRootPath: '.',
          scanStartPath: 'deployment',
          resolvePath: '/deployment/',
          useTitleFromFileHeading: true,
          includeRootIndexFile: true,
          collapsed: true,
          useFolderTitleFromIndexFile: true,
          useFolderLinkFromIndexFile: true,
          sortMenusByFrontmatterOrder: true,
          frontmatterOrderDefaultValue: 10
        }
      ]),

      socialLinks: [
        { icon: 'github', link: 'https://github.com/maplerime/cloudland' }
      ],

      footer: {
        message: '基于 Apache 2.0 许可证发布',
        copyright: 'Copyright © 2024-present CloudLand'
      },

      search: {
        provider: 'local',
        options: {
          translations: {
            button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
            modal: {
              noResultsText: '无法找到相关结果',
              resetButtonTitle: '清除查询条件',
              footer: { selectText: '选择', navigateText: '切换', closeText: '关闭' },
            }
          }
        }
      },

      editLink: {
        pattern: 'https://github.com/maplerime/cloudland/edit/main/docs/:path',
        text: '在 GitHub 上编辑此页'
      },

      docFooter: {
        prev: '上一页',
        next: '下一页'
      },

      outline: {
        label: '页面导航',
        level: [2, 3]
      },

      lastUpdated: {
        text: '最后更新于',
      },

      returnToTopLabel: '回到顶部',
      sidebarMenuLabel: '菜单',
      darkModeSwitchLabel: '主题',
    },

    mermaid: {
      theme: 'base',
      themeVariables: {
        primaryColor: '#e0f2fe',
        primaryTextColor: '#1a2332',
        primaryBorderColor: '#0ea5e9',
        lineColor: '#0ea5e9',
        secondaryColor: '#f0f9ff',
        tertiaryColor: '#ffffff',
        stateBkg: '#e0f2fe',
        stateBorder: '#0ea5e9',
        labelColor: '#1a2332',
        fontFamily: "'Inter', sans-serif",
        fontSize: '14px'
      },
      // Flattened config for better plugin compatibility
      flowchart: {
        padding: 30, // Increased padding
        useMaxWidth: false, // Don't force width, let layout grow
        htmlLabels: true,
        curve: 'basis'
      },
      sequence: {
        diagramMarginX: 60,
        diagramMarginY: 20,
        actorMargin: 60,
        width: 160,
        height: 70,
        boxMargin: 15,
        messageMargin: 40,
        mirrorActors: true
      }
    },

    mermaidPlugin: {
      class: 'mermaid',
    },
  })
)

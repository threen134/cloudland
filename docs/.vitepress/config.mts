import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

export default withMermaid(
  defineConfig({
    title: 'CloudLand',
    description: '轻量级 IaaS 云平台 — 文档中心',
    lang: 'zh-CN',
    lastUpdated: true,

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
        { text: '部署', link: '/deployment/quick-start' },
      ],

      sidebar: {
        '/guide/': [
          {
            text: '入门',
            collapsed: false,
            items: [
              { text: '项目简介', link: '/guide/introduction' },
              { text: '快速开始', link: '/guide/getting-started' },
              { text: '核心概念', link: '/guide/concepts' },
            ]
          },
          {
            text: '资源管理',
            collapsed: false,
            items: [
              { text: '虚拟机实例', link: '/guide/instances' },
              { text: '网络与 VPC', link: '/guide/networking' },
              { text: '存储卷', link: '/guide/volumes' },
              { text: '镜像管理', link: '/guide/images' },
              { text: '安全组', link: '/guide/security-groups' },
              { text: '负载均衡', link: '/guide/load-balancers' },
            ]
          },
          {
            text: '运维管理',
            collapsed: false,
            items: [
              { text: '组织与用户', link: '/guide/organizations' },
              { text: '计算节点管理', link: '/guide/hypervisors' },
              { text: '监控告警', link: '/guide/monitoring' },
            ]
          },
        ],

        '/api/': [
          {
            text: 'API 参考',
            collapsed: false,
            items: [
              { text: '概览', link: '/api/overview' },
              { text: '认证', link: '/api/authentication' },
              { text: '实例', link: '/api/instances' },
              { text: '网络', link: '/api/networks' },
              { text: '存储', link: '/api/storage' },
              { text: '镜像', link: '/api/images-api' },
              { text: '安全组', link: '/api/security-groups-api' },
            ]
          },
        ],

        '/architecture/': [
          {
            text: '系统架构',
            collapsed: false,
            items: [
              { text: '架构概览', link: '/architecture/overview' },
              { text: '控制面 (cland)', link: '/architecture/control-plane' },
              { text: 'API 服务层', link: '/architecture/api-layer' },
              { text: '计算节点生命周期', link: '/architecture/node-lifecycle' },
            ]
          },
        ],

        '/deployment/': [
          {
            text: '部署指南',
            collapsed: false,
            items: [
              { text: '快速部署', link: '/deployment/quick-start' },
              { text: '环境准备', link: '/deployment/prerequisites' },
              { text: 'Docker 部署', link: '/deployment/docker' },
              { text: '配置说明', link: '/deployment/configuration' },
            ]
          },
        ],
      },

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
      // Mermaid config options
    },

    mermaidPlugin: {
      class: 'mermaid',
    },
  })
)

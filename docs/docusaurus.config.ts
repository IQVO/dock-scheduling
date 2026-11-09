import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import type * as OpenApiPlugin from 'docusaurus-plugin-openapi-docs';

const config: Config = {
  title: 'Dock Scheduling',
  tagline:
    'Dock Scheduling: a Supporting bounded context of the WMS domain (warehouse-systems).',
  favicon: 'img/favicon.svg',

  future: {
    v4: true,
    faster: true,
  },

  url: 'https://iqvo.github.io',
  baseUrl: '/dock-scheduling/',

  organizationName: 'IQVO',
  projectName: 'dock-scheduling',
  deploymentBranch: 'gh-pages',
  trailingSlash: false,

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  markdown: {
    mermaid: true,
    // ADR bodies are immutable and were written before this site existed: parse
    // them as CommonMark (a stray `<name>` or `{x}` is not MDX there) and do not
    // fail the build on their links to files outside the docs tree. Every other
    // page is MDX and a broken link in it still throws.
    parseFrontMatter: async (params) => {
      const isAdr = /[\\/]docs[\\/]adr[\\/]/.test(params.filePath);
      let result;
      try {
        result = await params.defaultParseFrontMatter(params);
      } catch (e) {
        if (!isAdr) {
          throw e;
        }
        // An ADR whose front matter is not valid YAML (an unquoted colon in the
        // title): keep it readable, with the id taken from the file name.
        result = {frontMatter: {}, content: params.fileContent};
      }
      if (isAdr) {
        result.frontMatter.mdx = {...(result.frontMatter.mdx as object), format: 'md'};
        // Some ADRs carry `slug: /adr/NNNN-...` from an older layout; this instance already
        // serves them under /docs/adr/, so a front-matter slug would double the prefix.
        delete result.frontMatter.slug;
      }
      return result;
    },
    hooks: {
      onBrokenMarkdownLinks: ({sourceFilePath, url}) => {
        if (/^adr[\\/]|[\\/]docs[\\/]adr[\\/]/.test(sourceFilePath)) {
          return;
        }
        throw new Error(`Broken markdown link in ${sourceFilePath}: ${url}`);
      },
    },
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl:
            'https://github.com/IQVO/dock-scheduling/tree/main/docs/docs/',
          docItemComponent: '@theme/ApiItem',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  plugins: [
    // The ADRs stay in docs/adr/*.md (other repos link to those paths); this
    // second docs instance serves them in place under /docs/adr.
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'adr',
        path: 'adr',
        routeBasePath: 'docs/adr',
        sidebarPath: './sidebarsAdr.ts',
        // Keep the 0001- prefix in URLs/ids so they match the file names.
        numberPrefixParser: false,
        editUrl: 'https://github.com/IQVO/dock-scheduling/tree/main/docs/adr/',
      },
    ],
    [
      'docusaurus-plugin-openapi-docs',
      {
        id: 'openapi',
        docsPluginId: 'classic',
        config: {
          'dock-scheduling': {
            // The single source of truth: the same Spectral-linted spec CI
            // gates on. Never hand-transcribed here.
            specPath: '../apis/openapi.yaml',
            outputDir: 'docs/api-reference/rest',
            sidebarOptions: {
              groupPathsBy: 'tag',
              categoryLinkSource: 'tag',
            },
            hideSendButton: true,
          } satisfies OpenApiPlugin.Options,
        },
      },
    ],
  ],

  themes: ['docusaurus-theme-openapi-docs', '@docusaurus/theme-mermaid'],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Dock Scheduling',
      logo: {
        alt: 'Dock Scheduling',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
        {
          to: '/docs/api-reference',
          label: 'API Reference',
          position: 'left',
        },
        {
          to: '/docs/adr',
          label: 'ADRs',
          position: 'left',
        },
        {
          href: 'https://github.com/IQVO/dock-scheduling',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {label: 'Introduction', to: '/docs/overview/introduction'},
            {label: 'Quickstart', to: '/docs/overview/quickstart'},
            {label: 'Testing', to: '/docs/development/testing'},
            {label: 'API Reference', to: '/docs/api-reference'},
            {label: 'Architecture decisions', to: '/docs/adr'},
          ],
        },
        {
          title: 'Source',
          items: [
            {label: 'GitHub repository', href: 'https://github.com/IQVO/dock-scheduling'},
            {label: 'OpenAPI spec', href: 'https://raw.githubusercontent.com/IQVO/dock-scheduling/main/apis/openapi.yaml'},
            {label: 'AsyncAPI spec', href: 'https://raw.githubusercontent.com/IQVO/dock-scheduling/main/apis/asyncapi.yaml'},
          ],
        },
      ],
      copyright: `Dock Scheduling — a warehouse-systems application. Built ${new Date().getFullYear()}. This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'go', 'json', 'yaml', 'sql'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;

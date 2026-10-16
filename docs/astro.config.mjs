import { defineConfig, passthroughImageService } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://shridarpatil.github.io',
  base: '/tring',
  // No image processing: keeps sharp (a native dependency) out of the build.
  image: { service: passthroughImageService() },
  integrations: [
    starlight({
      title: 'Tring',
      description: 'Open-source, API-first voice calling',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/shridarpatil/tring' },
      ],
      customCss: ['./src/styles/custom.css'],
      sidebar: [
        {
          label: 'Getting Started',
          items: [
            { label: 'Introduction', slug: 'index' },
            { label: 'Quickstart', slug: 'getting-started/quickstart' },
          ],
        },
        {
          label: 'Guides',
          items: [{ label: 'WhatsApp calls over SIP', slug: 'guides/whatsapp-sip' }],
        },
        {
          label: 'Architecture',
          items: [
            { label: 'Overview', slug: 'architecture/overview' },
            { label: 'State & high availability', slug: 'architecture/state-and-ha' },
          ],
        },
        {
          label: 'Contributing',
          items: [{ label: 'Code style', slug: 'contributing/code-style' }],
        },
        {
          label: 'Design notes',
          collapsed: true,
          items: [
            { label: 'Call-log pagination', slug: 'design/call-log-pagination' },
            { label: 'Dashboard: Live Ops cockpit', slug: 'design/dashboard-live-ops-cockpit' },
          ],
        },
      ],
    }),
  ],
});

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
      // One light/dark toggle instead of the Auto/Dark/Light dropdown.
      components: { ThemeSelect: './src/components/ThemeToggle.astro' },
      sidebar: [
        {
          label: 'Getting Started',
          items: [
            { label: 'Introduction', slug: 'index' },
            { label: 'Quickstart', slug: 'getting-started/quickstart' },
            { label: 'Configuration', slug: 'getting-started/configuration' },
          ],
        },
        {
          label: 'Features',
          items: [
            { label: 'Web calling', slug: 'features/web-calling' },
            { label: 'IVR flows', slug: 'features/ivr-flows' },
            { label: 'Agents & routing', slug: 'features/agents-and-routing' },
            { label: 'Numbers & trunks', slug: 'features/numbers-and-trunks' },
            { label: 'Outbound calls & masking', slug: 'features/outbound-calls' },
            { label: 'Call control', slug: 'features/call-control' },
            { label: 'Recording & call logs', slug: 'features/recording-and-call-logs' },
            { label: 'Webhooks', slug: 'features/webhooks' },
            { label: 'Sounds & hold music', slug: 'features/sounds-and-hold-music' },
            { label: 'Tenants & auth', slug: 'features/multi-tenancy-and-auth' },
          ],
        },
        {
          label: 'Guides',
          items: [{ label: 'WhatsApp calls over SIP', slug: 'guides/whatsapp-sip' }],
        },
        {
          label: 'Browser SDK',
          items: [
            { label: 'Overview', slug: 'sdk/overview' },
            { label: 'Visitor widget (voice.js)', slug: 'sdk/visitor-widget' },
            { label: 'Agent widget (agent.js)', slug: 'sdk/agent-widget' },
            { label: 'JavaScript API', slug: 'sdk/javascript-api' },
          ],
        },
        {
          label: 'API Reference',
          items: [
            { label: 'Overview', slug: 'api-reference/overview' },
            { label: 'Authentication', slug: 'api-reference/authentication' },
            { label: 'API keys', slug: 'api-reference/api-keys' },
            { label: 'Tenant & guest config', slug: 'api-reference/tenant' },
            { label: 'Agents', slug: 'api-reference/agents' },
            { label: 'Teams', slug: 'api-reference/teams' },
            { label: 'Queues & stats', slug: 'api-reference/queues-and-stats' },
            { label: 'Audio', slug: 'api-reference/audio' },
            { label: 'Numbers', slug: 'api-reference/numbers' },
            { label: 'Trunks', slug: 'api-reference/trunks' },
            { label: 'Flows', slug: 'api-reference/flows' },
            { label: 'Calls', slug: 'api-reference/calls' },
            { label: 'Webhooks', slug: 'api-reference/webhooks' },
          ],
        },
        {
          label: 'Architecture',
          collapsed: true,
          items: [
            { label: 'Overview', slug: 'architecture/overview' },
            { label: 'State & high availability', slug: 'architecture/state-and-ha' },
          ],
        },
        {
          label: 'Contributing',
          collapsed: true,
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

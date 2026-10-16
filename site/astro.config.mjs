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
          items: [{ label: 'Introduction', slug: 'index' }],
        },
        {
          label: 'Guides',
          items: [{ label: 'WhatsApp calls over SIP', slug: 'guides/whatsapp-sip' }],
        },
      ],
    }),
  ],
});

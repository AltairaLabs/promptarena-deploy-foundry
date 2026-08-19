// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightThemeGalaxy from 'starlight-theme-galaxy';
import d2 from 'astro-d2';

// https://astro.build/config
export default defineConfig({
  integrations: [
    d2(),
    starlight({
      title: 'Foundry Adapter',
      plugins: [starlightThemeGalaxy()],
      customCss: ['./src/styles/custom.css'],
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/AltairaLabs/promptarena-deploy-foundry' },
      ],
      sidebar: [
        { label: 'Overview', link: '/' },
        { label: 'Tutorials', autogenerate: { directory: 'tutorials' } },
        { label: 'How-To Guides', autogenerate: { directory: 'how-to' } },
        { label: 'Explanation', autogenerate: { directory: 'explanation' } },
        { label: 'Reference', autogenerate: { directory: 'reference' } },
      ],
    }),
  ],
});

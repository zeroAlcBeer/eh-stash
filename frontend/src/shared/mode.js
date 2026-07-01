// Single source of truth for the self-hosted vs public (ehstash.com)
// build mode. Driven by Vite's --mode flag:
//   pnpm dev / pnpm build         → self-hosted (default)
//   pnpm dev:public / build:public → public (VITE_APP_MODE=public)
//
// Import IS_PUBLIC from here instead of reading import.meta.env scattered
// across components.

export const IS_PUBLIC = import.meta.env.VITE_APP_MODE === 'public';

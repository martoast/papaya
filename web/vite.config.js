import { defineConfig } from 'vite'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Everything is inlined or bundled: this app is served on a tailnet with no
    // guaranteed route to the internet, so a runtime fetch to any CDN would hang.
    assetsInlineLimit: 4096,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/upload': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
})

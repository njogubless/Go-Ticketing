import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    // The dev server proxies /api to the Go service so the browser sees a
    // single origin. That keeps development honest: no CORS-only-in-dev
    // workarounds that then have to be reproduced in production.
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://localhost:8080',
        changeOrigin: true,
      },
      '/api/v1/realtime': {
        target: process.env.VITE_API_TARGET ?? 'http://localhost:8080',
        ws: true,
        changeOrigin: true,
      },
    },
  },
  build: {
    sourcemap: true,
    rollupOptions: {
      output: {
        // Splitting the charting library out of the main bundle matters
        // because only one route uses it — an agent working the queue should
        // not download a plotting library to read a ticket.
        manualChunks: {
          charts: ['recharts'],
        },
      },
    },
  },
});

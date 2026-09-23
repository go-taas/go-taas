import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The console is served by taas-server's gateway on the same origin as the
// REST API (/api/v1/...), so no dev-server proxy is needed in production.
// For local development against a running compose stack, `npm run dev`
// proxies /api to the gateway.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET || 'http://127.0.0.1:9091',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
  },
});

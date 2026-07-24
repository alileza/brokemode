import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:9101',
      '/metrics': 'http://127.0.0.1:9101',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
});

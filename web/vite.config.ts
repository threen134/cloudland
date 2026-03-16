import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vite.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd())
  const apiTarget = env.VITE_API_PROXY_TARGET

  return {
    plugins: [vue()],
    optimizeDeps: {
      esbuildOptions: {
        target: 'esnext'
      }
    },
    build: {
      target: 'esnext'
    },
    server: apiTarget
      ? {
          proxy: {
            '/api': {
              target: apiTarget,
              changeOrigin: true,
              secure: false,
            },
            '/swagger': {
              target: apiTarget,
              changeOrigin: true,
              secure: false,
            }
          }
        }
      : undefined
  }
})

import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import Icons from "unplugin-icons/vite";
import { resolve } from "node:path";

export default defineConfig({
  plugins: [vue(), Icons({ compiler: "vue3", scale: 1 })],
  resolve: {
    alias: {
      "@": resolve(__dirname, "src")
    }
  },
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/__tests__/*.spec.ts"]
  }
});

import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// Relative asset paths: the page is served at the root of whatever host the
// estate gives it, but a relative base keeps `vite preview` and a sub-path
// mount working without a rebuild.
export default defineConfig({
  base: "./",
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  test: { include: ["src/**/*.test.ts", "src/**/*.test.tsx"] },
});

import uiPreset from 'nanocat-ui/tailwind-preset'
export default {
  presets: [uiPreset],
  content: [
    './index.html',
    './src/**/*.{vue,ts}',
  ],
  theme: { extend: {} },
  plugins: [],
}

/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // Cool-tinted dark scale with real elevation steps. Token names are
        // stable — pages reference these, so retuning values re-skins the app.
        dark: {
          bg: '#0b0d11',            // page ground
          surface: '#14171d',       // cards, sidebar, modals
          surfaceHover: '#1b1f27',  // hover on surface
          surfaceActive: '#222732', // pressed / selected
          inset: '#0e1015',         // wells inside cards (inputs, code)
          border: '#252a33',        // default hairline
          borderStrong: '#333a46',  // emphasized / hover border
          text: '#e7eaee',
          textSecondary: '#9aa3b2',
          textMuted: '#667082',
        },
        accent: {
          DEFAULT: '#3b82f6',
          hover: '#2f6fe0',
          soft: 'rgba(59, 130, 246, 0.12)',
        },
      },
      boxShadow: {
        // Soft elevation for modals / popovers on a dark ground.
        'elevated': '0 0 0 1px rgba(255,255,255,0.04), 0 10px 30px -10px rgba(0,0,0,0.7)',
        'card': '0 1px 2px rgba(0,0,0,0.3)',
      },
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'Helvetica Neue', 'Arial', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'SF Mono', 'Menlo', 'Consolas', 'Liberation Mono', 'monospace'],
      },
      keyframes: {
        'fade-in': {
          from: { opacity: '0' },
          to: { opacity: '1' },
        },
        'scale-in': {
          from: { opacity: '0', transform: 'scale(0.97) translateY(4px)' },
          to: { opacity: '1', transform: 'scale(1) translateY(0)' },
        },
      },
      animation: {
        'fade-in': 'fade-in 120ms ease-out',
        'scale-in': 'scale-in 140ms ease-out',
      },
    },
  },
  plugins: [],
}

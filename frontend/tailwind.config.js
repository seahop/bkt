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
        dark: {
          bg: '#0a0a0a',
          surface: '#1a1a1a',
          surfaceHover: '#252525',
          border: '#333333',
          text: '#e5e5e5',
          textSecondary: '#a3a3a3',
        },
      },
    },
  },
  plugins: [],
}

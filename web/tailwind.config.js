import typography from "@tailwindcss/typography";

/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        primary: {
          DEFAULT: "#094cb2",
          container: "#2b6fd3"
        },
        tertiary: {
          DEFAULT: "#6d5e00",
          container: "#f2e8a8"
        },
        surface: {
          DEFAULT: "#fbfaf6",
          dim: "#e5dfd6",
          bright: "#fffdf8",
          "container-lowest": "#ffffff",
          "container-low": "#f6f2eb",
          container: "#efe9df",
          "container-high": "#e7dfd3",
          "container-highest": "#ddd4c7"
        },
        "on-surface": {
          DEFAULT: "#211d17",
          variant: "#625b51",
          muted: "#7b746a",
          inverse: "#fbf5ec"
        },
        outline: {
          DEFAULT: "#7f7669",
          variant: "#8a8175"
        }
      },
      fontFamily: {
        sans: [
          "Inter",
          "ui-sans-serif",
          "system-ui",
          "-apple-system",
          "BlinkMacSystemFont",
          "Segoe UI",
          "sans-serif"
        ],
        serif: [
          "Noto Serif",
          "Georgia",
          "Cambria",
          "Times New Roman",
          "serif"
        ],
        label: [
          "Public Sans",
          "Inter",
          "ui-sans-serif",
          "system-ui",
          "sans-serif"
        ],
        mono: [
          "Geist Mono",
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "Monaco",
          "Consolas",
          "monospace"
        ]
      },
      borderRadius: {
        sm: "0.375rem",
        md: "0.5rem",
        lg: "0.75rem",
        xl: "1rem"
      }
    }
  },
  plugins: [typography]
};

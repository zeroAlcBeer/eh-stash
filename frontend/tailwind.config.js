/** @type {import('tailwindcss').Config} */
export default {
    content: [
        "./index.html",
        "./src/**/*.{js,jsx,ts,tsx}",
    ],
    darkMode: 'class',
    theme: {
        extend: {},
    },
    plugins: [],
    // 禁用 Preflight 避免与 MUI 冲突
    corePlugins: {
        preflight: false,
    },
}

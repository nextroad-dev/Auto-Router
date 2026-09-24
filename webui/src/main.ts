import { createApp } from 'vue'
import ui from '@nuxt/ui/vue-plugin'
import App from './App.vue'
import { router } from './router'
import { i18n } from './i18n'
// Generated from Fontsource CSS before dev/build/typecheck. It keeps Noto's unicode-range
// slices for remote cold-load efficiency and strips legacy WOFF sources to embed WOFF2 only.
import '../.generated/fonts.css'
import './theme/jude.css'
import './style.css'

const app = createApp(App)

app.use(router)
app.use(i18n)
app.use(ui)

app.mount('#app')

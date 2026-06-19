import { createApp } from "vue";
import App from "./App.vue";
import router from "./router";
import "./lib/theme"; // applies persisted theme to <html> before first paint
import "@fontsource-variable/jetbrains-mono";
import "./style.css";

createApp(App).use(router).mount("#app");

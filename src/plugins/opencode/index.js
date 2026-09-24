// OpenCode 2 resolves a plugin directory via <dir>/server or <dir>/index only;
// package.json main/exports are ignored. Re-export the real entrypoint.
export { default } from './plugin.js';

import { fileURLToPath } from 'node:url';
export default { resolve: { alias: { '@core': fileURLToPath(new URL('./src',import.meta.url)) } }, test: { setupFiles:['./tests/setup.ts'], fileParallelism:false, pool:'forks' } };

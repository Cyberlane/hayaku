import { cloudflareTest } from '@cloudflare/vitest-plugin';
export default {plugins:[cloudflareTest({wrangler:{configPath:'./wrangler.jsonc'}})],test:{fileParallelism:false}};

import { test, expect, beforeEach } from 'vitest';
import { env, SELF } from 'cloudflare:test';
beforeEach(async()=>{await env.STATE.delete('value')});
test('binding',()=>{expect(env.GREETING).toBe('ready')});
test('storage',async()=>{await env.STATE.put('value','7');expect(await env.STATE.get('value')).toBe('7')});
test('request',async()=>{const r=await SELF.fetch('https://fixture.test/value');expect(await r.text()).toBe('ready:/value')});
test('reset',async()=>{expect(await env.STATE.get('value')).toBe(null)});

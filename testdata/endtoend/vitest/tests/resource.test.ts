import { test, expect } from 'vitest';
import asset from '../src/asset.txt?raw';
test('asset',()=>{expect(asset).toBe('ready\n')});

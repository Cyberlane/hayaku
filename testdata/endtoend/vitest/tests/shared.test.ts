import { test, expect } from 'vitest';
import { shared } from '@core/shared';
test('shared',()=>{expect(shared()).toBe(14)});

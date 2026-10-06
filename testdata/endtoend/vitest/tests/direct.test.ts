import { test, expect } from 'vitest';
import { value } from '@core/value';
test('direct',()=>{expect(value()).toBe(7)});

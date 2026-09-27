import { describe, expect, it } from 'vitest';
import { SessionScope } from '@/modules/scope';

describe('会话作用域', () => {
  it('切会话后旧凭证失效，新凭证有效', () => {
    const scope = new SessionScope('a');
    const first = scope.current$();
    expect(first.alive()).toBe(true);

    scope.switchTo('b');
    expect(first.alive()).toBe(false);
    const second = scope.current$();
    expect(second.alive()).toBe(true);
    expect(second.sessionId).toBe('b');
  });

  it('A→B→A 时旧代次仍然失效', () => {
    // 这正是「按 URL 判断」会失效的场景：两个代次共用同一个 sessionId。
    const scope = new SessionScope('a');
    const first = scope.current$();
    scope.switchTo('b');
    scope.switchTo('a');
    expect(scope.current).toBe('a');
    expect(first.alive()).toBe(false);
    expect(scope.current$().alive()).toBe(true);
  });

  it('write 只在归属仍有效时执行', () => {
    const scope = new SessionScope('a');
    const first = scope.current$();
    let ran = 0;
    first.write(() => { ran++; });
    expect(ran).toBe(1);

    scope.switchTo('b');
    first.write(() => { ran++; });
    expect(ran).toBe(1);
  });

  it('代次只增不减', () => {
    const scope = new SessionScope('');
    const e0 = scope.epoch;
    scope.switchTo('a');
    const e1 = scope.epoch;
    scope.switchTo('b');
    expect(e1).toBeGreaterThan(e0);
    expect(scope.epoch).toBeGreaterThan(e1);
  });
});

import { describe, expect, it } from 'vitest';
import { languageFor } from '@/modules/highlight';

describe('代码高亮语言推断', () => {
  it('按扩展名映射到 hljs 语言名', () => {
    expect(languageFor('/a/b/app.ts')).toBe('typescript');
    expect(languageFor('/a/b/main.go')).toBe('go');
    expect(languageFor('/a/b/README.md')).toBe('markdown');
    expect(languageFor('/a/b/data.json')).toBe('json');
    expect(languageFor('/a/b/run.sh')).toBe('bash');
  });
  it('识别无扩展名的约定文件名', () => {
    expect(languageFor('/a/Dockerfile')).toBe('dockerfile');
    expect(languageFor('/a/dockerfile.prod')).toBe('dockerfile');
    expect(languageFor('/a/Makefile')).toBe('makefile');
  });
  it('未收录的扩展名返回空字符串，交给 hljs 自动判断', () => {
    expect(languageFor('/a/b/archive.tar.gz')).toBe('');
    expect(languageFor('/a/b/LICENSE')).toBe('');
    expect(languageFor('')).toBe('');
  });
});

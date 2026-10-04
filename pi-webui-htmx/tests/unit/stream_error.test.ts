import { describe, expect, it, vi } from 'vitest';
import { mount } from '../helpers/dom';
import { LiveView } from '@/modules/stream';

describe('供应商失败的实时预览', () => {
  it('不显示错误原文里的请求标识或密钥', () => {
    const { document, cleanup } = mount(`<div id="live" hidden><div id="live-user"></div><div id="live-flow"></div></div>`);
    const live = new LiveView(document.getElementById('live')!);
    live.begin('图像测试');
    live.event({ type: 'message_end', message: { stopReason: 'error', errorMessage: '402 request_id=private-id api_key=private-key' } });
    live.finish();
    const text = document.querySelector('.streaming-text')!;
    expect(text.textContent).toContain('模型请求失败');
    expect(text.textContent).not.toContain('private-id');
    expect(text.textContent).not.toContain('private-key');
    live.dispose();
    vi.restoreAllMocks();
    cleanup();
  });
});

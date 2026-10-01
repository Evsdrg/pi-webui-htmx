import { describe, expect, it, vi } from 'vitest';
import { mount } from '../helpers/dom';
import { LiveView } from '@/modules/stream';

describe('供应商失败的实时预览', () => {
  it('不显示错误原文里的请求标识或密钥', () => {
    const { document, cleanup } = mount(`<div id="live" hidden><div id="live-user"></div><div id="live-text"></div><div id="live-thinking"></div><div id="live-tools"></div></div>`);
    const live = new LiveView(document.getElementById('live')!);
    live.begin('图像测试');
    live.event({ type: 'message_end', message: { stopReason: 'error', errorMessage: '402 request_id=private-id api_key=private-key' } });
    live.finish();
    expect(document.getElementById('live-text')!.textContent).toContain('模型请求失败');
    expect(document.getElementById('live-text')!.textContent).not.toContain('private-id');
    expect(document.getElementById('live-text')!.textContent).not.toContain('private-key');
    live.dispose();
    vi.restoreAllMocks();
    cleanup();
  });
});

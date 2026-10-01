import { afterEach, expect, it, vi } from 'vitest';
import { wireLazy } from '@/modules/lazy';
let dispose:()=>void=()=>{};
afterEach(()=>{dispose();vi.unstubAllGlobals();document.body.replaceChildren();});
it('惰性图片未加载就卸载也释放Blob且取消请求',async()=>{
 document.body.innerHTML='<button class="lazy-block" data-lazy="tool-image" data-entry-id="e" data-block-index="0">图片</button>';
 const revoke=vi.fn();vi.stubGlobal('URL',class extends URL {static createObjectURL=()=> 'blob:test';static revokeObjectURL=revoke;});
 vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response('image')));dispose=wireLazy(()=> 's');document.querySelector<HTMLButtonElement>('button')!.click();
 await vi.waitFor(()=>expect(document.querySelector('img')).not.toBeNull());dispose();expect(revoke).toHaveBeenCalledWith('blob:test');
});

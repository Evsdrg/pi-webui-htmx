// marked 的类型垫片。@types/marked 已废弃且与新版不匹配，
// 这里只声明我们用到的两个成员，避免把整个废弃类型树拉进来。
export interface DefaultExport {
  marked: {
    setOptions(options: { gfm: boolean; breaks: boolean }): void;
    parse(src: string, options: { async: false }): string;
  };
}
